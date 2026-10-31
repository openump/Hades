// hades-inspector serves a strictly read-only local commissioning console. It
// reads only what Hades already exposes read-only and never touches an
// actuator control socket or a PLC.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/infer"
	"github.com/openump/Hades/internal/inspect"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/supervisor"
	"github.com/openump/Hades/internal/transport"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	root := flag.String("root", ".", "contract root")
	config := flag.String("config", "examples/site/metadata.json", "reviewed metadata snapshot (comma-separated catalog)")
	broker := flag.String("broker", "tls://127.0.0.1:8883", "mTLS broker")
	listen := flag.String("listen", "127.0.0.1:18091", "loopback console listener")
	normalizer := flag.String("normalizer-health", "", "optional normalizer https://.../health for quarantine counts")
	certs := flag.String("certificates", "", "comma-separated PEM certificates to watch for expiry")
	services := flag.String("services", "", "comma-separated supervisor service.json paths")
	actuatorKeys := flag.String("actuator-keys", "", "actuator public keyring; receipts that fail verification are not shown")
	credentials := transport.Flags()
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("-listen must bind a loopback IP; the console is local-only")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := sample.LoadContext(*root, *config)
	if err != nil {
		return err
	}
	cfg.TLS, err = transport.Client(*credentials)
	if err != nil {
		return err
	}
	if cfg.TLS == nil {
		return fmt.Errorf("hades-inspector requires mTLS broker credentials")
	}
	validator, err := contracts.New(os.DirFS(filepath.Join(*root, "schemas")))
	if err != nil {
		return err
	}
	var trust map[string]ed25519.PublicKey
	if *actuatorKeys != "" {
		if trust, err = actuator.LoadPublicKeys(*actuatorKeys); err != nil {
			return err
		}
	}
	model := inspect.NewModel(func() time.Time { return time.Now().UTC() })
	broadcaster := inspect.NewBroadcaster()
	var topics []paho.SubscribeOptions
	for _, asset := range cfg.Assets() {
		id, _ := asset.Metadata["asset_id"].(string)
		intervals := map[string]time.Duration{}
		if defs, ok := asset.Metadata["measurements"].(map[string]any); ok {
			for name, raw := range defs {
				d, _ := raw.(map[string]any)
				intervals[name] = time.Duration(sample.Num(d["expected_interval_ms"])) * time.Millisecond
			}
		}
		model.Declare(id, strings.TrimSuffix(asset.Prefix, "/"), intervals)
		topics = append(topics, paho.SubscribeOptions{Topic: asset.Prefix + "telemetry", QoS: 1}, paho.SubscribeOptions{Topic: asset.Prefix + "status", QoS: 1})
		if trust != nil {
			topics = append(topics, paho.SubscribeOptions{Topic: asset.Prefix + "command_receipt/+", QoS: 1})
		}
	}
	manager, err := subscribe(ctx, *broker, cfg, topics, func(topic string, raw []byte) {
		observe(topic, raw, validator, cfg, trust, model)
	})
	if err != nil {
		return err
	}
	defer manager.Disconnect(context.Background())
	// Change notifications are coalesced per stream and never block.
	model.OnChange(broadcaster.Notify)
	go pollDiagnostics(ctx, model, cfg, *normalizer, *certs, *services)

	srv := &http.Server{Addr: *listen, Handler: inspect.NewHandler(model, broadcaster, 15*time.Second, inspect.LoopbackHosts(*listen)...), ReadHeaderTimeout: 5 * time.Second}
	fmt.Println(`{"type":"inspector_ready","listen":"` + *listen + `"}`)
	return inspect.Serve(ctx, srv)
}

// observe validates a broker message and records it. Telemetry and status are
// contract-validated and bound to their topic; receipts are shown only when
// signed by a trusted actuator key.
func observe(topic string, raw []byte, validator *contracts.Validator, cfg *sample.Config, trust map[string]ed25519.PublicKey, model *inspect.Model) {
	if len(raw) > contracts.MaxPayloadBytes {
		return
	}
	switch {
	case strings.HasSuffix(topic, "/telemetry"):
		payload, err := validator.Validate("telemetry", raw)
		if err != nil || cfg.Registry.ValidateTopic(topic, stringOf(payload["asset_id"])) != nil {
			return
		}
		model.Telemetry(payload)
	case strings.HasSuffix(topic, "/status"):
		payload, err := validator.Validate("status", raw)
		if err != nil || cfg.Registry.ValidateTopic(topic, stringOf(payload["asset_id"])) != nil {
			return
		}
		model.Status(stringOf(payload["asset_id"]), raw)
	case strings.Contains(topic, "/command_receipt/"):
		receipt, err := validator.Validate("command-receipt", raw)
		if err != nil || actuator.Verify(raw, trust, true) != nil {
			return // never display an unverified gate result
		}
		// The receipt must belong to the asset and parameter of the topic it came on.
		if cfg.Registry.ValidateControlTopic(topic, stringOf(receipt["asset"]), stringOf(receipt["target_parameter"]), "command_receipt") != nil {
			return
		}
		gates, _ := json.Marshal(receipt["gates"])
		terminal, _ := receipt["terminal"].(bool)
		model.Receipt(inspect.Receipt{CommandID: stringOf(receipt["command_id"]), Asset: stringOf(receipt["asset"]), Parameter: stringOf(receipt["target_parameter"]),
			State: stringOf(receipt["state"]), Terminal: terminal, Gates: gates, RecordedAt: time.UnixMilli(int64(sample.Num(receipt["recorded_at_ms"]))).UTC()})
	}
}

func subscribe(ctx context.Context, broker string, cfg *sample.Config, topics []paho.SubscribeOptions, handle func(string, []byte)) (*autopaho.ConnectionManager, error) {
	u, err := transport.BrokerURL(broker, cfg.TLS)
	if err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	conf := autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: cfg.TLS, KeepAlive: 10, ConnectTimeout: 3 * time.Second, ClientConfig: paho.ClientConfig{ClientID: "hades-inspector-" + infer.UUIDv7(time.Now())[24:], PacketTimeout: 3 * time.Second}}
	conf.OnConnectionUp = func(m *autopaho.ConnectionManager, _ *paho.Connack) {
		go func() {
			c, done := context.WithTimeout(ctx, 3*time.Second)
			defer done()
			ack, err := m.Subscribe(c, &paho.Subscribe{Subscriptions: topics})
			if err == nil {
				for _, reason := range ack.Reasons {
					if reason > 1 {
						err = fmt.Errorf("hades-inspector subscription denied")
					}
				}
			}
			select {
			case ready <- err:
			default:
				if err != nil {
					log.Printf(`{"type":"inspector_degraded","reason":"resubscribe failed: %v"}`, err)
				}
			}
		}()
	}
	conf.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(e paho.PublishReceived) (bool, error) {
		handle(e.Packet.Topic, e.Packet.Payload)
		return true, nil
	}}
	manager, err := autopaho.NewConnection(ctx, conf)
	if err != nil {
		return nil, err
	}
	select {
	case err := <-ready:
		if err != nil {
			manager.Disconnect(context.Background())
			return nil, err
		}
	case <-time.After(10 * time.Second):
		manager.Disconnect(context.Background())
		return nil, fmt.Errorf("broker subscription timed out")
	}
	return manager, nil
}

// pollDiagnostics refreshes quarantine counts, certificate expiry and service
// health from their read-only sources.
func pollDiagnostics(ctx context.Context, model *inspect.Model, cfg *sample.Config, normalizer, certs, services string) {
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg.TLS, Proxy: nil}}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if normalizer != "" {
			if stats, err := normalizerHealth(ctx, client, normalizer); err == nil {
				model.SetQuarantine(stats)
			} else {
				model.SetQuarantine(map[string]any{"error": err.Error()})
			}
		}
		model.SetCertificates(certificates(certs))
		model.SetServices(serviceHealth(ctx, services))
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// normalizerHealth reads only the quarantine and rejection counters.
func normalizerHealth(ctx context.Context, client *http.Client, endpoint string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("normalizer health: HTTP %d (is hades-inspector in the normalizer's -ingress-readers?)", resp.StatusCode)
	}
	var all map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&all); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, k := range []string{"quarantine_records", "quarantine_bytes", "quarantine_evictions", "rejected", "storage_failures", "disk_reserve_ok"} {
		out[k] = all[k]
	}
	return out, nil
}

func certificates(paths string) []inspect.Certificate {
	var out []inspect.Certificate
	for _, path := range strings.Split(paths, ",") {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		days := int(math.Floor(time.Until(cert.NotAfter).Hours() / 24))
		out = append(out, inspect.Certificate{Path: path, Subject: cert.Subject.CommonName, NotAfter: cert.NotAfter.UTC(), DaysLeft: days})
	}
	return out
}

// serviceHealth probes each supervised service concurrently, keyed by its
// definition path so services sharing a revision never overwrite each other.
func serviceHealth(ctx context.Context, paths string) map[string]any {
	out := map[string]any{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, path := range strings.Split(paths, ",") {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			entry := map[string]any{}
			if cfg, err := supervisor.Load(path); err != nil {
				entry["error"] = err.Error()
			} else if status, err := supervisor.Probe(ctx, cfg); err != nil {
				entry["revision"], entry["ready"], entry["error"] = cfg.Revision, false, err.Error()
			} else {
				entry["revision"], entry["ready"], entry["last_output_at"] = cfg.Revision, status.Ready, status.LastOutput
			}
			mu.Lock()
			out[path] = entry
			mu.Unlock()
		}(path)
	}
	wg.Wait()
	return out
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
