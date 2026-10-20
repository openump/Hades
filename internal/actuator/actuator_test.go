package actuator

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
)

type fakePLC struct {
	mu         sync.Mutex
	writes     int
	heartbeats int
	value      float64
	seq        uint16
	reject     uint16
	noAck      bool
}

func (p *fakePLC) Stage(ctx context.Context, b Binding, m Mailbox, seq uint16, value float64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	p.writes++
	p.seq = seq
	p.value = value
	return nil
}
func (p *fakePLC) Readback(ctx context.Context, b Binding, m Mailbox) (float64, uint16, uint16, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.noAck {
		return p.value, 0, 0, nil
	}
	return p.value, p.reject, p.seq, nil
}
func (p *fakePLC) Heartbeat(ctx context.Context, b Binding, m Mailbox, n uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	p.heartbeats++
	return nil
}
func (p *fakePLC) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writes, p.heartbeats
}

type fixture struct {
	e       *Engine
	o       Options
	now     time.Time
	key     ed25519.PrivateKey
	command map[string]any
	plc     *fakePLC
	topic   string
}

func setup(t *testing.T, level string) *fixture {
	t.Helper()
	cfg, err := sample.LoadContext("../..", "../../examples/site/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	_, receipt, _ := ed25519.GenerateKey(rand.Reader)
	raw, err := os.ReadFile("../../examples/contracts/control/actuation-policy.json")
	if err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	json.Unmarshal(raw, &policy)
	policy["site_autonomy_level"] = "L3"
	a := policy["assets"].([]any)[0].(map[string]any)
	a["autonomy_level"] = "L3"
	p := a["parameters"].(map[string]any)["pressure_setpoint"].(map[string]any)
	p["required_preconditions"].([]any)[0].(map[string]any)["measurement"] = "motor_temperature"
	p["required_preconditions"].([]any)[0].(map[string]any)["unit"] = "Cel"
	p["required_preconditions"].([]any)[0].(map[string]any)["value"] = 4.0
	a["parameters"] = map[string]any{"motor_temperature": p}
	p["unit"] = "Cel"
	policyRaw, _ := json.Marshal(policy)
	raw, _ = os.ReadFile("../../examples/contracts/control/command.json")
	var command map[string]any
	json.Unmarshal(raw, &command)
	now := time.Now()
	command["issued_at_ms"] = now.UnixMilli()
	command["target_parameter"] = "motor_temperature"
	command["unit"] = "Cel"
	command["origin"].(map[string]any)["autonomy_level"] = level
	command["preconditions"] = p["required_preconditions"]
	plc := &fakePLC{value: 4}
	f := &fixture{now: now, key: key, command: command, plc: plc, topic: cfg.Prefix + "command/motor_temperature"}
	f.o = Options{Config: cfg, Policy: policyRaw, Keys: map[string]ed25519.PublicKey{"operator-key": pub}, SigningKey: receipt, SigningKeyID: "actuator-key", Directory: filepath.Join(t.TempDir(), "state"), Bindings: []Binding{{Asset: a["asset"].(string), Parameter: "motor_temperature", Endpoint: "127.0.0.1:1502", AckSequence: "holding:105"}}, PLC: plc, Now: func() time.Time { return f.now }, Verification: 50 * time.Millisecond, Heartbeat: 50 * time.Millisecond}
	f.e, err = Open(f.o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f.e != nil {
			f.e.Close()
		}
	})
	f.e.telemetry[a["asset"].(string)+"/motor_temperature"] = Observation{Value: 4, Unit: "Cel", Quality: "good", At: now, Received: now, Uncertainty: 0}
	return f
}
func (f *fixture) signed(t *testing.T) []byte {
	t.Helper()
	raw, _ := json.Marshal(f.command)
	raw, err := Sign(raw, f.key, false)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func state(t *testing.T, raw []byte) string {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p["state"].(string)
}
func TestGateRejectionsHaveZeroFieldbusCalls(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(*fixture)
	}{
		{"expired", "REJECTED_EXPIRED_TTL", func(f *fixture) { f.command["issued_at_ms"] = f.now.Add(-time.Minute).UnixMilli() }},
		{"future", "REJECTED_EXPIRED_TTL", func(f *fixture) { f.command["issued_at_ms"] = f.now.Add(time.Second).UnixMilli() }},
		{"bounds", "REJECTED_SAFETY_BOUNDS", func(f *fixture) { f.command["target_value"] = 99 }},
		{"slew", "REJECTED_SAFETY_BOUNDS", func(f *fixture) { f.command["target_value"] = 8 }},
		{"stale", "REJECTED_SAFETY_BOUNDS", func(f *fixture) { f.now = f.now.Add(2 * time.Second) }},
		{"unknown-clock", "REJECTED_SAFETY_BOUNDS", func(f *fixture) {
			for k, v := range f.e.telemetry {
				v.Uncertainty = -1
				f.e.telemetry[k] = v
			}
		}},
		{"interlock", "REJECTED_PRECONDITION_FAILED", func(f *fixture) { f.command["preconditions"].([]any)[0].(map[string]any)["value"] = 3 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, "L2")
			tc.change(f)
			receipt, err := f.e.Handle(context.Background(), f.topic, f.signed(t), false)
			if err != nil {
				t.Fatal(err)
			}
			if got := state(t, receipt); got != tc.want {
				t.Fatal(got)
			}
			if w, h := f.plc.counts(); w != 0 || h != 0 {
				t.Fatal(w, h)
			}
			if err = Verify(receipt, map[string]ed25519.PublicKey{"actuator-key": f.o.SigningKey.Public().(ed25519.PublicKey)}, true); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestShadowAndConfirmation(t *testing.T) {
	f := setup(t, "L0")
	receipt, err := f.e.Handle(context.Background(), f.topic, f.signed(t), false)
	if err != nil || state(t, receipt) != "SHADOW_LOGGED" {
		t.Fatal(err, string(receipt))
	}
	if w, h := f.plc.counts(); w != 0 || h != 0 {
		t.Fatal(w, h)
	}
	g := setup(t, "L1")
	receipt, err = g.e.Handle(context.Background(), g.topic, g.signed(t), false)
	if err != nil || state(t, receipt) != "AWAITING_OPERATOR_CONFIRMATION" {
		t.Fatal(err, string(receipt))
	}
	if w, h := g.plc.counts(); w != 0 || h != 0 {
		t.Fatal(w, h)
	}
	own := g.e.owned[g.command["command_id"].(string)]
	receipt, err = g.e.Confirm(context.Background(), g.command["command_id"].(string), own.intent.Digest)
	if err != nil || state(t, receipt) != "EXECUTED" {
		t.Fatal(err, string(receipt))
	}
}
func TestMailboxOutcomesReplayCooldownAndRestart(t *testing.T) {
	for _, want := range []string{"EXECUTED", "PLC_INTERLOCK_REFUSED", "TIMEOUT"} {
		t.Run(want, func(t *testing.T) {
			f := setup(t, "L2")
			f.plc.reject = map[string]uint16{"PLC_INTERLOCK_REFUSED": 9}[want]
			f.plc.noAck = want == "TIMEOUT"
			raw := f.signed(t)
			receipt, err := f.e.Handle(context.Background(), f.topic, raw, false)
			if err != nil || state(t, receipt) != want {
				t.Fatal(err, string(receipt))
			}
			again, err := f.e.Handle(context.Background(), f.topic, raw, false)
			if err != nil || string(again) != string(receipt) {
				t.Fatal(err)
			}
			if w, _ := f.plc.counts(); w != 1 {
				t.Fatal(w)
			}
			f.command["command_id"] = "019a1234-5678-7000-8000-000000000099"
			second, err := f.e.Handle(context.Background(), f.topic, f.signed(t), false)
			if err != nil || state(t, second) != "REJECTED_SAFETY_BOUNDS" {
				t.Fatal(err, string(second))
			}
			if err = f.e.Close(); err != nil {
				t.Fatal(err)
			}
			f.e, err = Open(f.o)
			if err != nil {
				t.Fatal(err)
			}
			again, err = f.e.Handle(context.Background(), f.topic, raw, false)
			if err != nil || string(again) != string(receipt) {
				t.Fatal(err)
			}
			if w, _ := f.plc.counts(); w != 1 {
				t.Fatal(w)
			}
		})
	}
}
func TestAuditFailureAndKillFailClosed(t *testing.T) {
	f := setup(t, "L2")
	f.e.audit.store.Close()
	if _, err := f.e.Handle(context.Background(), f.topic, f.signed(t), false); err == nil {
		t.Fatal("audit failure accepted")
	}
	if w, h := f.plc.counts(); w != 0 || h != 0 {
		t.Fatal(w, h)
	}
	g := setup(t, "L2")
	if err := g.e.Kill(); err != nil {
		t.Fatal(err)
	}
	receipt, err := g.e.Handle(context.Background(), g.topic, g.signed(t), false)
	if err != nil || state(t, receipt) != "SHADOW_LOGGED" {
		t.Fatal(err, string(receipt))
	}
	if w, h := g.plc.counts(); w != 0 || h != 0 {
		t.Fatal(w, h)
	}
	g.e.Close()
	g.e, err = Open(g.o)
	if err != nil || !g.e.stopped.Load() {
		t.Fatal(err)
	}
}
func TestSignatureTamperAndRetained(t *testing.T) {
	f := setup(t, "L2")
	raw := f.signed(t)
	var obj map[string]any
	json.Unmarshal(raw, &obj)
	obj["target_value"] = 6
	tampered, _ := json.Marshal(obj)
	if _, err := f.e.Handle(context.Background(), f.topic, tampered, false); err == nil {
		t.Fatal("tampered signature accepted")
	}
	receipt, err := f.e.Handle(context.Background(), f.topic, raw, true)
	if err != nil || state(t, receipt) != "REJECTED_EXPIRED_TTL" {
		t.Fatal(err, string(receipt))
	}
}
func TestWatchdogStopsOnContextCancellation(t *testing.T) {
	f := setup(t, "L2")
	if _, err := f.e.Handle(context.Background(), f.topic, f.signed(t), false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.e.RunWatchdog(ctx) }()
	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done
	_, before := f.plc.counts()
	time.Sleep(100 * time.Millisecond)
	_, after := f.plc.counts()
	if before < 2 || after != before {
		t.Fatal(before, after)
	}
}
func TestCanonicalizationAndCommandBinding(t *testing.T) {
	f := setup(t, "L0")
	raw := f.signed(t)
	var pretty any
	json.Unmarshal(raw, &pretty)
	padded, _ := json.MarshalIndent(pretty, "", " ")
	if err := Verify(padded, f.o.Keys, false); err != nil {
		t.Fatal(err)
	}
	a, _ := Canonical(raw)
	b, _ := Canonical(padded)
	if string(a) != string(b) {
		t.Fatal("JCS mismatch")
	}
	if err := Verify([]byte(fmt.Sprintf(`{"x":1,"x":2}`)), f.o.Keys, false); err == nil {
		t.Fatal("duplicate keys allowed")
	}
}

func TestUnfinishedIntentRecoveryDoesNotReplay(t *testing.T) {
	f := setup(t, "L2")
	raw := f.signed(t)
	canonical, _ := Canonical(raw)
	in := intent{Command: canonical, Topic: f.topic, Digest: fmt.Sprintf("%x", sha256.Sum256(canonical)), At: f.now.UnixMilli(), Sequence: 1, Gates: []Gate{{1, "PASS", "verified"}, {2, "PASS", "verified"}, {3, "PASS", "verified"}, {4, "PASS", "verified"}, {5, "PASS", "synced"}}}
	if err := f.e.audit.Append("intent", in); err != nil {
		t.Fatal(err)
	}
	f.e.Close()
	var err error
	f.e, err = Open(f.o)
	if err != nil {
		t.Fatal(err)
	}
	own := f.e.owned[f.command["command_id"].(string)]
	if state(t, own.Receipt) != "TIMEOUT" {
		t.Fatal(string(own.Receipt))
	}
	if w, h := f.plc.counts(); w != 0 || h != 0 {
		t.Fatal(w, h)
	}
}
func TestLocalControlKillPersistsAndHealth(t *testing.T) {
	f := setup(t, "L2")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	short, err := os.MkdirTemp("/tmp", "h33-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	socket := filepath.Join(short, "control.sock")
	go func() { done <- f.e.ServeControl(ctx, socket) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := Control(ctx, socket, "health", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Control(ctx, socket, "kill", "", ""); err != nil {
		t.Fatal(err)
	}
	if !f.e.stopped.Load() {
		t.Fatal("kill did not set shadow")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUncertainNewCommandCannotInheritWatchdogLease(t *testing.T) {
	f := setup(t, "L2")
	if _, err := f.e.Handle(context.Background(), f.topic, f.signed(t), false); err != nil {
		t.Fatal(err)
	}
	if f.e.lease.IsZero() {
		t.Fatal("successful command has no lease")
	}
	p := f.e.policy.Assets[0].Parameters["motor_temperature"]
	p.Cooldown = 0
	f.e.policy.Assets[0].Parameters["motor_temperature"] = p
	for k, v := range f.e.telemetry {
		v.At = f.now
		v.Received = f.now
		f.e.telemetry[k] = v
	}
	f.command["issued_at_ms"] = f.now.UnixMilli()
	f.command["command_id"] = "019a1234-5678-7000-8000-000000000077"
	f.plc.noAck = true
	receipt, err := f.e.Handle(context.Background(), f.topic, f.signed(t), false)
	if err != nil || state(t, receipt) != "TIMEOUT" {
		t.Fatal(err, string(receipt))
	}
	if !f.e.lease.IsZero() {
		t.Fatal("uncertain command inherited a watchdog lease")
	}
}

func TestInvalidReceiptIdentityRefusedBeforeServiceStartup(t *testing.T) {
	f := setup(t, "L0")
	o := f.o
	o.Directory = filepath.Join(t.TempDir(), "other")
	o.SigningKeyID = "bad/key"
	if _, err := Open(o); err == nil {
		t.Fatal("invalid receipt key ID accepted")
	}
	o.SigningKeyID = "actuator-key"
	o.SigningKey = append(ed25519.PrivateKey(nil), o.SigningKey...)
	o.SigningKey[63] ^= 1
	if _, err := Open(o); err == nil {
		t.Fatal("inconsistent private/public key accepted")
	}
}
