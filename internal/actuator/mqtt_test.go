package actuator

import (
	"context"
	"encoding/json"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMTLSCommandAndReceiptBrokerPath(t *testing.T) {
	broker := os.Getenv("HADES_ACTUATOR_TEST_BROKER")
	pki := os.Getenv("HADES_ACTUATOR_TEST_PKI")
	if broker == "" || pki == "" {
		t.Skip("explicit disposable mTLS broker required")
	}
	f := setup(t, "L2")
	load := func(role string) *autopaho.ConnectionManager {
		t.Helper()
		tls, err := transport.Client(transport.Paths{CA: filepath.Join(pki, "ca.crt"), Cert: filepath.Join(pki, role+".crt"), Key: filepath.Join(pki, role+".key")})
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(broker)
		m, err := autopaho.NewConnection(context.Background(), autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: tls, ConnectTimeout: time.Second, ClientConfig: paho.ClientConfig{ClientID: "hades-033-test-" + role, PacketTimeout: time.Second}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			m.Disconnect(ctx)
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err = m.AwaitConnection(ctx); err != nil {
			t.Fatal(err)
		}
		return m
	}
	tls, err := transport.Client(transport.Paths{CA: filepath.Join(pki, "ca.crt"), Cert: filepath.Join(pki, "hades-actuator.crt"), Key: filepath.Join(pki, "hades-actuator.key")})
	if err != nil {
		t.Fatal(err)
	}
	f.e.opts.Config.TLS = tls
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.e.RunMQTT(ctx, broker) }()
	defer func() { cancel(); <-done }()
	for i := 0; i < 100 && !f.e.mqttReady.Load(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !f.e.mqttReady.Load() {
		t.Fatal("actuator did not subscribe")
	}
	proposer := load("command-proposer")
	normalizer := load("normalizer")
	f.e.mu.Lock()
	f.e.telemetry = map[string]Observation{}
	f.e.mu.Unlock()
	raw, err := f.o.Config.Observation("normal", 1, sample.UUID(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	json.Unmarshal(raw, &obj)
	obj["measurements"].(map[string]any)["motor_temperature"].(map[string]any)["value"] = 4
	raw, _ = json.Marshal(obj)
	if _, err = normalizer.Publish(ctx, &paho.Publish{Topic: f.o.Config.Prefix + "telemetry", Payload: raw, QoS: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	command := f.signed(t)
	if _, err = proposer.Publish(ctx, &paho.Publish{Topic: f.topic, Payload: command, QoS: 1}); err != nil {
		t.Fatal(err)
	}
	var receipt []byte
	for i := 0; i < 100; i++ {
		f.e.mu.Lock()
		own := f.e.owned[f.command["command_id"].(string)]
		if own != nil {
			receipt = append([]byte(nil), own.Receipt...)
		}
		f.e.mu.Unlock()
		if len(receipt) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state(t, receipt) != "EXECUTED" {
		t.Fatal(string(receipt))
	}
	for i := 0; i < 100 && len(f.e.PendingReceipts()) > 0; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if len(f.e.PendingReceipts()) != 0 {
		t.Fatal("signed receipt not broker acknowledged")
	}
	if w, _ := f.plc.counts(); w != 1 {
		t.Fatal(w)
	}
	if _, err = proposer.Publish(ctx, &paho.Publish{Topic: f.topic, Payload: command, QoS: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if w, _ := f.plc.counts(); w != 1 {
		t.Fatal("replay wrote", w)
	}
	if err = f.e.Kill(); err != nil {
		t.Fatal(err)
	}
	f.command["command_id"] = "019a1234-5678-7000-8000-000000000088"
	if _, err = proposer.Publish(ctx, &paho.Publish{Topic: f.topic, Payload: f.signed(t), QoS: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		f.e.mu.Lock()
		own := f.e.owned[f.command["command_id"].(string)]
		if own != nil {
			receipt = append([]byte(nil), own.Receipt...)
		} else {
			receipt = nil
		}
		f.e.mu.Unlock()
		if len(receipt) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if state(t, receipt) != "SHADOW_LOGGED" {
		t.Fatal(string(receipt))
	}
	if w, _ := f.plc.counts(); w != 1 {
		t.Fatal(w)
	}
}
