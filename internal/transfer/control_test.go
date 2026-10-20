package transfer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"github.com/openump/Hades/internal/actuator"
	"os"
	"testing"
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
