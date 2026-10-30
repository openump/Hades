// hades-infer runs one signed, budgeted Edge AI model next to the edge stack.
// It reads telemetry and publishes inference events and soft-sensor values on
// their own MQTT subtrees. It never writes device telemetry and has no
// command-plane access.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/infer"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	root := flag.String("root", ".", "contract root")
	config := flag.String("config", "examples/site/metadata.json", "reviewed metadata snapshot")
	bundleDir := flag.String("bundle", "", "verified model bundle directory")
	rollbackDir := flag.String("rollback-bundle", "", "optional known-good bundle for rollback_to_version")
	keys := flag.String("keys", "", "model release public keyring (key_id -> base64 Ed25519)")
	runnerSocket := flag.String("runner-socket", "", "local Unix socket for onnx_v1/external_runner_v1 models")
	broker := flag.String("broker", "tls://127.0.0.1:8883", "mTLS broker")
	interval := flag.Duration("interval", time.Second, "inference cadence, 100ms..1h")
	credentials := transport.Flags()
	flag.Parse()
	if *bundleDir == "" || *keys == "" {
		return fmt.Errorf("-bundle and -keys are required")
	}
	if *interval < 100*time.Millisecond || *interval > time.Hour {
		return fmt.Errorf("-interval must be 100ms..1h")
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
		return fmt.Errorf("hades-infer requires mTLS credentials")
	}
	validator, err := contracts.New(os.DirFS(*root + "/schemas"))
	if err != nil {
		return err
	}
	public, err := actuator.LoadPublicKeys(*keys)
	if err != nil {
		return err
	}
	trust := infer.Trust(public)
	status := statusReporter()
	model, err := loadModel(*bundleDir, validator, trust, *runnerSocket)
	if err != nil {
		status(infer.StatusEvent{State: infer.StateDegraded, Reason: "bundle-rejected: " + err.Error(), Model: "standby", At: time.Now().UTC()})
		return err
	}
	var fallback *infer.Model
	if *rollbackDir != "" {
		if fallback, err = loadModel(*rollbackDir, validator, trust, *runnerSocket); err != nil {
			return fmt.Errorf("rollback bundle: %w", err)
		}
	}
	engine, err := infer.NewEngine(model, fallback, status)
	if err != nil {
		return err
	}
	prefixes := map[string]string{}
	for _, asset := range cfg.Assets() {
		if id, ok := asset.Metadata["asset_id"].(string); ok {
			prefixes[id] = asset.Prefix
		}
	}
	topicPrefix := func(asset string) (string, error) {
		if p, ok := prefixes[asset]; ok {
			return p, nil
		}
		return "", fmt.Errorf("asset %s is not in the approved catalog", asset)
	}
	return serve(ctx, engine, validator, cfg, *broker, *interval, topicPrefix)
}

func loadModel(dir string, validator *contracts.Validator, trust infer.Trust, socket string) (*infer.Model, error) {
	bundle, err := infer.LoadBundle(dir, validator, trust)
	if err != nil {
		return nil, err
	}
	runner, err := infer.NewRunner(bundle, socket)
	if err != nil {
		return nil, err
	}
	return &infer.Model{Bundle: bundle, Runner: runner}, nil
}

func statusReporter() func(infer.StatusEvent) {
	encoder := json.NewEncoder(os.Stdout)
	return func(event infer.StatusEvent) {
		_ = encoder.Encode(map[string]any{"type": "producer_status", "producer": "hades-infer", "state": event.State, "reason": event.Reason, "model": event.Model, "reported_at": event.At.Format(sample.Timestamp)})
	}
}

func serve(ctx context.Context, engine *infer.Engine, validator *contracts.Validator, cfg *sample.Config, broker string, interval time.Duration, topicPrefix func(string) (string, error)) error {
	u, err := transport.BrokerURL(broker, cfg.TLS)
	if err != nil {
		return err
	}
	owner, cancel := context.WithCancel(ctx)
	defer cancel()
	var ready, observed, rejected, events, derived, failures, publishFailures, dropped atomic.Uint64
	failed := make(chan error, 1)
	report := func(err error) {
		select {
		case failed <- err:
		default:
		}
		cancel()
	}
	// Subscribe to every input of the active and fallback models, so a rollback
	// never leaves the fallback's windows unfed.
	var topics []paho.SubscribeOptions
	for _, asset := range engine.InputAssets() {
		prefix, err := topicPrefix(asset)
		if err != nil {
			return err
		}
		topics = append(topics, paho.SubscribeOptions{Topic: prefix + "telemetry", QoS: 1})
	}
	conf := autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: cfg.TLS, KeepAlive: 10, ConnectTimeout: 3 * time.Second, ClientConfig: paho.ClientConfig{ClientID: "hades-infer-" + strings.SplitN(engine.Active(), "@", 2)[0], PacketTimeout: 3 * time.Second}}
	conf.OnConnectionUp = func(m *autopaho.ConnectionManager, _ *paho.Connack) {
		go func() {
			c, done := context.WithTimeout(owner, 3*time.Second)
			defer done()
			ack, err := m.Subscribe(c, &paho.Subscribe{Subscriptions: topics})
			if err != nil {
				report(err)
				return
			}
			for _, reason := range ack.Reasons {
				if reason > 1 {
					report(fmt.Errorf("hades-infer telemetry subscription denied"))
					return
				}
			}
			ready.Store(1)
		}()
	}
	conf.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(event paho.PublishReceived) (bool, error) {
		p := event.Packet
		if p.Retain || len(p.Payload) > contracts.MaxPayloadBytes || !strings.HasSuffix(p.Topic, "/telemetry") {
			return true, nil
		}
		if n, err := engine.ObserveTelemetry(validator, cfg.Registry, p.Topic, p.Payload); err != nil {
			rejected.Add(1)
		} else {
			observed.Add(uint64(n))
		}
		return true, nil
	}}
	manager, err := autopaho.NewConnection(owner, conf)
	if err != nil {
		return err
	}
	defer manager.Disconnect(context.Background())
	// Publishing runs off the main loop so a stalled broker can never block
	// ticks, health reporting or shutdown. The queue is bounded: on overflow a
	// publication is dropped and counted rather than queued without limit.
	type outbound struct {
		pub   infer.Publication
		count *atomic.Uint64
	}
	queue := make(chan outbound, 64)
	go func() {
		for {
			select {
			case <-owner.Done():
				return
			case item := <-queue:
				c, done := context.WithTimeout(owner, 3*time.Second)
				ack, err := manager.Publish(c, &paho.Publish{Topic: item.pub.Topic, Payload: item.pub.Payload, QoS: 1})
				done()
				if err != nil || (ack != nil && ack.ReasonCode >= 0x80) {
					publishFailures.Add(1)
					continue
				}
				item.count.Add(1)
			}
		}
	}()
	enqueue := func(pub infer.Publication, count *atomic.Uint64) {
		select {
		case queue <- outbound{pub, count}:
		default:
			dropped.Add(1)
		}
	}
	checker := infer.Checker{Validator: validator, Registry: cfg.Registry}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	health := time.NewTicker(time.Second)
	defer health.Stop()
	announced := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failed:
			return err
		case <-health.C:
			if !announced && ready.Load() == 1 {
				fmt.Println(`{"type":"infer_ready"}`)
				announced = true
			}
			log.Print(mustJSON(map[string]any{"type": "infer_health", "model": engine.Active(), "observed": observed.Load(), "rejected_telemetry": rejected.Load(), "events": events.Load(), "derived": derived.Load(), "inference_failures": failures.Load(), "publish_failures": publishFailures.Load(), "publish_dropped": dropped.Load()}))
		case <-tick.C:
			out, err := engine.Step(ctx)
			if err != nil {
				failures.Add(1)
				continue
			}
			if out == nil {
				continue
			}
			emitted := time.Now().UTC()
			if emitted.Before(out.WindowEnd) {
				emitted = out.WindowEnd
			}
			// Build and check every publication for the window before sending any,
			// using the producing model's manifest so a rollback uses its own
			// bindings. A window that cannot be published in full counts against
			// the model's failure budget.
			pubs, sensors, err := infer.Publications(out.Manifest, out, emitted, topicPrefix)
			var derivedPubs []infer.Publication
			if err == nil {
				derivedPubs, err = infer.DerivedPublications(sensors, out, topicPrefix)
			}
			for _, pub := range pubs {
				if err == nil {
					err = checker.Event(pub)
				}
			}
			for _, pub := range derivedPubs {
				if err == nil {
					err = checker.Derived(pub)
				}
			}
			if err != nil {
				failures.Add(1)
				engine.ReportFailure(out, err)
				continue
			}
			for _, pub := range pubs {
				enqueue(pub, &events)
			}
			for _, pub := range derivedPubs {
				enqueue(pub, &derived)
			}
		}
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}
