package actuator

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/transport"
)

// LoadPublicKeys loads local operator-provisioned trust, never a command-supplied key.
func LoadPublicKeys(path string) (map[string]ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("oversized keyring")
	}
	var encoded map[string]string
	if json.Unmarshal(raw, &encoded) != nil || len(encoded) < 1 || len(encoded) > 64 {
		return nil, fmt.Errorf("invalid keyring")
	}
	out := map[string]ed25519.PublicKey{}
	for id, value := range encoded {
		key, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("invalid public key")
		}
		out[id] = ed25519.PublicKey(key)
	}
	return out, nil
}
func LoadSigningKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 256 {
		return nil, fmt.Errorf("signing key requires private regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 64 {
		return nil, fmt.Errorf("invalid Ed25519 private key")
	}
	return ed25519.PrivateKey(key), nil
}

func (e *Engine) ServeControl(ctx context.Context, path string) error {
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 {
		return fmt.Errorf("control directory must be private 0700")
	}
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		return fmt.Errorf("control socket exists; verify old service stopped before removing")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if err = os.Chmod(path, 0600); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(e.Stats()) })
	mux.HandleFunc("/kill", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if err := e.Kill(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"shadow_locked": true})
	})
	mux.HandleFunc("/confirm", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var request struct {
			ID     string `json:"command_id"`
			Digest string `json:"command_sha256"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil {
			http.Error(w, "invalid confirmation", 400)
			return
		}
		raw, err := e.Confirm(r.Context(), request.ID, request.Digest)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: 8 * time.Second, MaxHeaderBytes: 4096}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			server.Close()
		case <-done:
		}
	}()
	defer close(done)
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
func Control(ctx context.Context, socket, action, id, digest string) ([]byte, error) {
	if action != "health" && action != "kill" && action != "confirm" {
		return nil, fmt.Errorf("invalid action")
	}
	body, _ := json.Marshal(map[string]string{"command_id": id, "command_sha256": digest})
	method := "POST"
	if action == "health" {
		method = "GET"
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}, Timeout: 8 * time.Second}
	request, _ := http.NewRequestWithContext(ctx, method, "http://localhost/"+action, strings.NewReader(string(body)))
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("local actuator: HTTP %d: %s", response.StatusCode, raw)
	}
	return raw, err
}

// RunMQTT never executes retained commands or resumes an actuation backlog.
// The bounded command queue remains broker-owned until a synced intent/receipt.
func (e *Engine) RunMQTT(ctx context.Context, broker string) error {
	u, err := transport.BrokerURL(broker, e.opts.Config.TLS)
	if err != nil {
		return err
	}
	owner, cancel := context.WithCancel(ctx)
	defer cancel()
	type incoming struct {
		packet *paho.Publish
		client *paho.Client
	}
	queue := make(chan incoming, 32)
	errors := make(chan error, 1)
	report := func(err error) {
		select {
		case errors <- err:
		default:
		}
		cancel()
		e.Stop()
	}
	topics := []paho.SubscribeOptions{}
	for _, asset := range e.opts.Config.Assets() {
		topics = append(topics, paho.SubscribeOptions{Topic: asset.Prefix + "command/+", QoS: 1, RetainAsPublished: true}, paho.SubscribeOptions{Topic: asset.Prefix + "telemetry", QoS: 1, RetainAsPublished: true})
	}
	config := autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: e.opts.Config.TLS, KeepAlive: 10, ConnectTimeout: 3 * time.Second, SessionExpiryInterval: 86400, ClientConfig: paho.ClientConfig{ClientID: "hades-actuator-" + e.opts.SigningKeyID, EnableManualAcknowledgment: true, SendAcksInterval: 10 * time.Millisecond, PacketTimeout: 3 * time.Second}}
	config.OnClientError = func(err error) { e.mqttReady.Store(false); e.Stop() }
	config.OnConnectionUp = func(m *autopaho.ConnectionManager, _ *paho.Connack) {
		go func() {
			c, done := context.WithTimeout(owner, 3*time.Second)
			defer done()
			ack, err := m.Subscribe(c, &paho.Subscribe{Subscriptions: topics})
			if err != nil {
				report(err)
				return
			}
			if len(ack.Reasons) != len(topics) {
				report(fmt.Errorf("incomplete actuator SUBACK"))
				return
			}
			for _, reason := range ack.Reasons {
				if reason > 1 {
					report(fmt.Errorf("actuator subscription denied"))
					return
				}
			}
			e.mqttReady.Store(true)
		}()
	}
	config.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(event paho.PublishReceived) (bool, error) {
		p := event.Packet
		if len(p.Payload) > 65536 || p.QoS != 1 {
			return true, nil
		}
		if strings.HasSuffix(p.Topic, "/telemetry") {
			if !p.Retain {
				_ = e.Observe(p.Topic, p.Payload)
			}
			_ = event.Client.Ack(p)
			return true, nil
		}
		select {
		case queue <- incoming{p, event.Client}:
		default:
			report(fmt.Errorf("actuator command capacity exceeded"))
		}
		return true, nil
	}}
	manager, err := autopaho.NewConnection(owner, config)
	if err != nil {
		return err
	}
	defer func() {
		c, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		manager.Disconnect(c)
	}()
	go func() {
		for {
			select {
			case <-owner.Done():
				return
			case item := <-queue:
				receipt, err := e.Handle(owner, item.packet.Topic, item.packet.Payload, item.packet.Retain)
				if err != nil || len(receipt) == 0 {
					report(fmt.Errorf("command refused or unresolved: %v", err))
					return
				}
				if err = item.client.Ack(item.packet); err != nil {
					report(err)
					return
				}
			}
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errors:
			return err
		case <-owner.Done():
			return owner.Err()
		case <-ticker.C:
			for hash, raw := range e.PendingReceipts() {
				var r struct {
					Asset     string `json:"asset"`
					Parameter string `json:"target_parameter"`
				}
				json.Unmarshal(raw, &r)
				topic := ""
				for _, a := range e.opts.Config.Assets() {
					if a.Metadata["asset_id"] == r.Asset {
						topic = a.Prefix + "command_receipt/" + r.Parameter
					}
				}
				if topic == "" {
					return fmt.Errorf("receipt has no registry topic")
				}
				c, done := context.WithTimeout(owner, time.Second)
				ack, err := manager.Publish(c, &paho.Publish{Topic: topic, Payload: raw, QoS: 1, Retain: false})
				done()
				if err != nil {
					break
				}
				if ack == nil || ack.ReasonCode >= 128 {
					return fmt.Errorf("receipt publication refused")
				}
				if err = e.Published(hash); err != nil {
					return err
				}
			}
		}
	}
}
