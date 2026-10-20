package transfer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/transport"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSignedReceiptIntakeReplayAndExactTransfer(t *testing.T) {
	cfg, _ := fixture(t)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	raw, err := os.ReadFile("../../examples/contracts/control/command-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	json.Unmarshal(raw, &obj)
	obj["signature"].(map[string]any)["key_id"] = "actuator-key"
	raw, _ = json.Marshal(obj)
	raw, err = actuator.Sign(raw, key, true)
	if err != nil {
		t.Fatal(err)
	}
	topic := cfg.Prefix + "command_receipt/pressure_setpoint"
	keys := map[string]ed25519.PublicKey{"actuator-key": pub}
	dir := t.TempDir() + "/queue"
	q, err := OpenWithReceiptKeys(cfg, dir, 10, 1024*1024, keys)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Intake(topic, raw); err != nil {
		t.Fatal(err)
	}
	if err = q.Intake(topic, raw); err != nil {
		t.Fatal(err)
	}
	if len(q.records) != 1 {
		t.Fatal(q.records)
	}
	q.Close()
	q, err = OpenWithReceiptKeys(cfg, dir, 10, 1024*1024, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	calls := 0
	if err = q.Drain(context.Background(), func(_ context.Context, r Record) error {
		calls++
		if r.Topic != topic || string(r.Payload) != string(raw) || r.ID != ReceiptMessageID(raw) {
			t.Fatal(r)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(q.records) != 0 {
		t.Fatal(calls)
	}
	if _, err = ValidateWithReceiptKeys(cfg, topic, raw, nil); err == nil {
		t.Fatal("untrusted receipt accepted")
	}
	obj["unit"] = "Cel"
	tampered, _ := json.Marshal(obj)
	if _, err = ValidateWithReceiptKeys(cfg, topic, tampered, keys); err == nil {
		t.Fatal("tampered signature accepted")
	}
}

func TestSignedReceiptBrokerIntake(t *testing.T) {
	broker, pki := os.Getenv("HADES_ACTUATOR_TEST_BROKER"), os.Getenv("HADES_ACTUATOR_TEST_PKI")
	if broker == "" || pki == "" {
		t.Skip("explicit disposable mTLS broker required")
	}
	cfg, _ := fixture(t)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	raw, err := os.ReadFile("../../examples/contracts/control/command-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	json.Unmarshal(raw, &obj)
	obj["signature"].(map[string]any)["key_id"] = "receipt-broker-key"
	raw, _ = json.Marshal(obj)
	raw, err = actuator.Sign(raw, key, true)
	if err != nil {
		t.Fatal(err)
	}
	load := func(role string) *tls.Config {
		c, err := transport.Client(transport.Paths{CA: filepath.Join(pki, "ca.crt"), Cert: filepath.Join(pki, role+".crt"), Key: filepath.Join(pki, role+".key")})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	cfg.TLS = load("transfer-adapter")
	q, err := OpenWithReceiptKeys(cfg, t.TempDir()+"/queue", 100, 1024*1024, map[string]ed25519.PublicKey{"receipt-broker-key": pub})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	sub, err := q.Subscribe(ctx, broker, func(_ string, _ []byte, err error) error { return err })
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	u, _ := url.Parse(broker)
	m, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: load("hades-actuator"), ClientConfig: paho.ClientConfig{ClientID: "hades033-receipt-publisher"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		m.Disconnect(c)
	}()
	if err = m.AwaitConnection(ctx); err != nil {
		t.Fatal(err)
	}
	topic := cfg.Prefix + "command_receipt/pressure_setpoint"
	if _, err = m.Publish(ctx, &paho.Publish{Topic: topic, Payload: raw, QoS: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		q.mu.Lock()
		_, ok := q.records[ReceiptMessageID(raw)]
		q.mu.Unlock()
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	q.mu.Lock()
	record, ok := q.records[ReceiptMessageID(raw)]
	if ok {
		record, err = q.loadRecord(record.ID, record)
	}
	q.mu.Unlock()
	if !ok || err != nil || string(record.Payload) != string(raw) {
		t.Fatal("receipt did not reach synced transfer intake", err)
	}
}
