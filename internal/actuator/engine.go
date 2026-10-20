package actuator

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openump/Hades/internal/sample"
)

type Condition struct {
	Measurement string  `json:"measurement"`
	Operator    string  `json:"operator"`
	Value       float64 `json:"value"`
	Unit        string  `json:"unit"`
	MaxAge      int64   `json:"max_age_ms"`
}
type Parameter struct {
	Unit       string      `json:"unit"`
	Min        float64     `json:"min_value"`
	Max        float64     `json:"max_value"`
	Delta      float64     `json:"max_delta_per_step"`
	Cooldown   int64       `json:"cooldown_seconds"`
	Conditions []Condition `json:"required_preconditions"`
	Mailbox    Mailbox     `json:"mailbox"`
}
type Origin struct {
	Actor     string `json:"actor_id"`
	Level     string `json:"autonomy_level"`
	Signature struct {
		Key string `json:"key_id"`
	} `json:"signature"`
}
type Command struct {
	ID         string      `json:"command_id"`
	Issued     int64       `json:"issued_at_ms"`
	TTL        int64       `json:"ttl_ms"`
	Revision   int64       `json:"policy_revision"`
	Origin     Origin      `json:"origin"`
	Asset      string      `json:"asset"`
	Parameter  string      `json:"target_parameter"`
	Value      float64     `json:"target_value"`
	Unit       string      `json:"unit"`
	Conditions []Condition `json:"preconditions"`
}
type AssetPolicy struct {
	Asset   string `json:"asset"`
	Level   string `json:"autonomy_level"`
	Origins []struct {
		Actor string `json:"actor_id"`
		Key   string `json:"key_id"`
	} `json:"allowed_origins"`
	Parameters map[string]Parameter `json:"parameters"`
}
type Policy struct {
	Revision   int64         `json:"policy_revision"`
	Enterprise string        `json:"enterprise"`
	Site       string        `json:"site"`
	Level      string        `json:"site_autonomy_level"`
	Assets     []AssetPolicy `json:"assets"`
}
type Gate struct {
	Gate   int    `json:"gate"`
	Result string `json:"result"`
	Reason string `json:"reason"`
}
type Observation struct {
	Value         float64
	Unit, Quality string
	At, Received  time.Time
	Uncertainty   float64
}
type intent struct {
	Command  json.RawMessage        `json:"command"`
	Topic    string                 `json:"topic"`
	Digest   string                 `json:"digest"`
	At       int64                  `json:"at_ms"`
	Sequence uint16                 `json:"request_sequence"`
	Gates    []Gate                 `json:"gates"`
	Snapshot map[string]Observation `json:"snapshot"`
}
type owned struct {
	intent  intent
	Receipt []byte
	Pending bool
}
type Options struct {
	Config       *sample.Config
	Policy       []byte
	Keys         map[string]ed25519.PublicKey
	SigningKey   ed25519.PrivateKey
	SigningKeyID string
	Directory    string
	Bindings     []Binding
	PLC          PLC
	Now          func() time.Time
	Verification time.Duration
	Heartbeat    time.Duration
}
type Engine struct {
	mqttReady       atomic.Bool
	mu              sync.Mutex
	opts            Options
	policy          Policy
	audit           *Audit
	owned           map[string]*owned
	telemetry       map[string]Observation
	last            map[string]time.Time
	bindings        map[string]Binding
	sent            map[string]bool
	stopped         atomic.Bool
	cancelMu        sync.Mutex
	activeCancel    context.CancelFunc
	lease           time.Time
	leaseKey        string
	leaseConditions []Condition
	counter         uint16
	sequence        uint16
}

func Open(o Options) (*Engine, error) {
	if o.Config == nil || o.PLC == nil || len(o.SigningKey) != 64 || o.SigningKeyID == "" {
		return nil, fmt.Errorf("config, PLC, receipt identity required")
	}
	if _, err := o.Config.Validator.Validate("actuation-policy", o.Policy); err != nil {
		return nil, err
	}
	var policy Policy
	if err := json.Unmarshal(o.Policy, &policy); err != nil {
		return nil, err
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Verification == 0 {
		o.Verification = time.Second
	}
	if o.Heartbeat == 0 {
		o.Heartbeat = 250 * time.Millisecond
	}
	if o.Verification < time.Millisecond || o.Verification > 5*time.Second || o.Heartbeat < 50*time.Millisecond || o.Heartbeat > time.Second {
		return nil, fmt.Errorf("invalid execution budgets")
	}
	e := &Engine{opts: o, policy: policy, owned: map[string]*owned{}, telemetry: map[string]Observation{}, last: map[string]time.Time{}, bindings: map[string]Binding{}, sent: map[string]bool{}}
	addresses := map[string]bool{}
	for _, b := range o.Bindings {
		key := b.Asset + "/" + b.Parameter
		if _, ok := e.bindings[key]; ok {
			return nil, fmt.Errorf("duplicate binding")
		}
		a, p, ok := e.parameter(b.Asset, b.Parameter)
		if !ok {
			return nil, fmt.Errorf("binding not in policy")
		}
		_ = a
		if err := ValidateBinding(b, p.Mailbox); err != nil {
			return nil, err
		}
		for _, s := range []string{p.Mailbox.Proposed, p.Mailbox.Request, p.Mailbox.Watchdog, p.Mailbox.Active, p.Mailbox.Reject, b.AckSequence} {
			addr, _ := address(s)
			physical := fmt.Sprintf("%s/%d", b.Endpoint, addr)
			if addresses[physical] {
				return nil, fmt.Errorf("overlapping physical mailbox registers")
			}
			addresses[physical] = true
		}
		e.bindings[key] = b
	}
	audit, err := OpenAudit(filepath.Join(o.Directory, "audit"), o.SigningKey)
	if err != nil {
		return nil, err
	}
	e.audit = audit
	for _, r := range audit.Records {
		switch r.Kind {
		case "intent":
			var i intent
			if json.Unmarshal(r.Body, &i) != nil {
				audit.Close()
				return nil, fmt.Errorf("invalid intent")
			}
			var c Command
			if json.Unmarshal(i.Command, &c) != nil {
				audit.Close()
				return nil, fmt.Errorf("invalid owned command")
			}
			e.owned[c.ID] = &owned{intent: i}
			if i.Sequence > e.sequence {
				e.sequence = i.Sequence
			}
			if i.Sequence > 0 {
				e.last[c.Asset+"/"+c.Parameter] = time.UnixMilli(i.At)
			}
		case "receipt":
			var obj map[string]any
			if json.Unmarshal(r.Body, &obj) != nil {
				audit.Close()
				return nil, fmt.Errorf("invalid receipt")
			}
			id, _ := obj["command_id"].(string)
			if own := e.owned[id]; own != nil {
				own.Receipt = append([]byte(nil), r.Body...)
				own.Pending = obj["state"] == "AWAITING_OPERATOR_CONFIRMATION"
			}
		case "published":
			var h string
			json.Unmarshal(r.Body, &h)
			e.sent[h] = true
		}
	}
	if err = e.recoverUnfinished(); err != nil {
		audit.Close()
		return nil, err
	}
	// Restart never automatically resumes an execution or a watchdog lease.
	if _, err := os.Stat(filepath.Join(o.Directory, "shadow.lock")); err == nil {
		e.stopped.Store(true)
	} else if !os.IsNotExist(err) {
		audit.Close()
		return nil, err
	}
	return e, nil
}
func (e *Engine) parameter(asset, name string) (AssetPolicy, Parameter, bool) {
	for _, a := range e.policy.Assets {
		if a.Asset == asset {
			p, ok := a.Parameters[name]
			return a, p, ok
		}
	}
	return AssetPolicy{}, Parameter{}, false
}
func (e *Engine) Close() error { e.Stop(); e.mu.Lock(); defer e.mu.Unlock(); return e.audit.Close() }
func (e *Engine) Stop() {
	e.stopped.Store(true)
	e.cancelMu.Lock()
	if e.activeCancel != nil {
		e.activeCancel()
	}
	e.cancelMu.Unlock()
}
func (e *Engine) Kill() error {
	e.Stop()
	f, err := os.OpenFile(filepath.Join(e.opts.Directory, "shadow.lock"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	dir, err := os.Open(e.opts.Directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (e *Engine) Observe(topic string, raw []byte) error {
	cfg, kind, err := e.opts.Config.ResolveTopic(topic)
	if err != nil || kind != "telemetry" {
		return fmt.Errorf("unapproved telemetry topic")
	}
	obj, err := e.opts.Config.Validator.Validate("telemetry", raw)
	if err != nil {
		return err
	}
	if err = e.opts.Config.Registry.ValidateTopic(topic, obj["asset_id"].(string)); err != nil {
		return err
	}
	if err = validateTelemetry(obj, cfg); err != nil {
		return err
	}
	stamp, err := time.Parse(time.RFC3339Nano, obj["observed_at"].(string))
	if err != nil {
		return err
	}
	source := obj["source"].(map[string]any)
	uncertainty, ok := numberOK(source["clock_uncertainty_ms"])
	if !ok {
		uncertainty = -1
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for name, item := range obj["measurements"].(map[string]any) {
		r := item.(map[string]any)
		v, ok := numberOK(r["value"])
		quality, _ := r["quality"].(string)
		if !ok {
			quality = "bad"
		}
		key := obj["asset_id"].(string) + "/" + name
		// Replayed/out-of-order telemetry cannot refresh an interlock.
		if previous, exists := e.telemetry[key]; exists && !stamp.After(previous.At) {
			continue
		}
		e.telemetry[key] = Observation{Value: v, Unit: r["unit"].(string), Quality: quality, At: stamp, Received: e.opts.Now(), Uncertainty: uncertainty}
	}
	return nil
}
func numberOK(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	}
	return 0, false
}
func (e *Engine) fresh(key string, age int64) (Observation, bool) {
	v, ok := e.telemetry[key]
	now := e.opts.Now()
	limit := time.Duration(age) * time.Millisecond
	delta := now.Sub(v.At)
	return v, ok && v.Quality == "good" && v.Uncertainty >= 0 && v.Uncertainty < float64(age) && !math.IsInf(v.Uncertainty, 0) && delta >= 0 && delta+time.Duration(v.Uncertainty*float64(time.Millisecond)) < limit && now.Sub(v.Received) >= 0 && now.Sub(v.Received) < limit
}
func compare(a float64, op string, b float64) bool {
	switch op {
	case "eq":
		return a == b
	case "ne":
		return a != b
	case "lt":
		return a < b
	case "le":
		return a <= b
	case "gt":
		return a > b
	case "ge":
		return a >= b
	}
	return false
}
func (e *Engine) conditions(c Command, p Parameter) bool {
	for _, required := range p.Conditions {
		found := false
		for _, supplied := range c.Conditions {
			if required == supplied {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	for _, condition := range c.Conditions {
		v, ok := e.fresh(c.Asset+"/"+condition.Measurement, condition.MaxAge)
		if !ok || v.Unit != condition.Unit || !compare(v.Value, condition.Operator, condition.Value) {
			return false
		}
	}
	return true
}
func (e *Engine) Handle(ctx context.Context, topic string, raw []byte, retained bool) ([]byte, error) {
	return e.handle(ctx, topic, raw, retained, false)
}
func (e *Engine) Confirm(ctx context.Context, id, digest string) ([]byte, error) {
	e.mu.Lock()
	own := e.owned[id]
	if own == nil || !own.Pending || own.intent.Digest != digest {
		e.mu.Unlock()
		return nil, fmt.Errorf("no matching pending confirmation")
	}
	raw := append([]byte(nil), own.intent.Command...)
	topic := own.intent.Topic
	e.mu.Unlock()
	return e.handle(ctx, topic, raw, false, true)
}
func (e *Engine) handle(ctx context.Context, topic string, raw []byte, retained, confirmed bool) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	obj, err := e.opts.Config.Validator.Validate("command", raw)
	if err != nil {
		return nil, err
	}
	_ = obj
	var c Command
	json.Unmarshal(raw, &c)
	if err = e.opts.Config.Registry.ValidateControlTopic(topic, c.Asset, c.Parameter, "command"); err != nil {
		return nil, err
	}
	canonical, err := Canonical(raw)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(canonical))
	if err = Verify(raw, e.opts.Keys, false); err != nil {
		return nil, err
	}
	own := e.owned[c.ID]
	if own != nil {
		if own.intent.Digest != digest {
			return nil, fmt.Errorf("command identity conflict")
		}
		if !confirmed {
			return append([]byte(nil), own.Receipt...), nil
		}
		if !own.Pending {
			return nil, fmt.Errorf("command not pending")
		}
	}
	gates := make([]Gate, 5)
	for i := range gates {
		gates[i] = Gate{i + 1, "NOT_EVALUATED", "not reached"}
	}
	failGate := func(i int, reason string) { gates[i-1] = Gate{i, "FAIL", reason} }
	pass := func(i int) { gates[i-1] = Gate{i, "PASS", "verified"} }
	now := e.opts.Now()
	a, p, allowed := e.parameter(c.Asset, c.Parameter)
	key := c.Asset + "/" + c.Parameter
	state := ""
	var before, after any
	if retained || now.UnixMilli() < c.Issued || now.UnixMilli()-c.Issued >= c.TTL {
		failGate(1, "retained, future or expired lease")
		state = "REJECTED_EXPIRED_TTL"
	} else {
		pass(1)
	}
	authorized := false
	for _, o := range a.Origins {
		if o.Actor == c.Origin.Actor && o.Key == c.Origin.Signature.Key {
			authorized = true
		}
	}
	scope := strings.Split(topic, "/")
	if state == "" {
		if !allowed || !authorized || c.Revision != e.policy.Revision || scope[2] != e.policy.Enterprise || scope[3] != e.policy.Site || c.Origin.Level > a.Level || c.Origin.Level > e.policy.Level {
			failGate(2, "origin, policy, autonomy or parameter refused")
			state = "REJECTED_SAFETY_BOUNDS"
		} else {
			pass(2)
		}
	}
	level := c.Origin.Level
	if e.stopped.Load() {
		level = "L0"
	}
	if state == "" && level == "L0" {
		state = "SHADOW_LOGGED"
	}
	if state == "" && level == "L1" && !confirmed {
		state = "AWAITING_OPERATOR_CONFIRMATION"
	}
	if state == "" {
		current, ok := e.fresh(key, 1000)
		before = current.Value
		if !ok || current.Unit != p.Unit || c.Unit != p.Unit || c.Value < p.Min || c.Value > p.Max || math.Abs(c.Value-current.Value) > p.Delta || now.Sub(e.last[key]) < time.Duration(p.Cooldown)*time.Second || c.Value < 0 || c.Value > 65535 || math.Trunc(c.Value) != c.Value {
			failGate(3, "bounds, slew, cooldown or current-value freshness refused")
			state = "REJECTED_SAFETY_BOUNDS"
		} else {
			pass(3)
		}
	}
	if state == "" {
		if !e.conditions(c, p) {
			failGate(4, "missing, unsafe or stale live preconditions")
			state = "REJECTED_PRECONDITION_FAILED"
		} else {
			pass(4)
		}
	}
	sequence := uint16(0)
	if state == "" {
		if _, ok := e.bindings[key]; !ok || e.sequence == 65535 {
			failGate(2, "unbound mailbox or sequence exhaustion")
			state = "REJECTED_SAFETY_BOUNDS"
		} else {
			e.sequence++
			sequence = e.sequence
		}
	}
	gates[4] = Gate{5, "PASS", "synced signed audit intent"}
	in := intent{canonical, topic, digest, now.UnixMilli(), sequence, gates, map[string]Observation{}}
	for k, v := range e.telemetry {
		if strings.HasPrefix(k, c.Asset+"/") {
			in.Snapshot[k] = v
		}
	}
	if err = e.audit.Append("intent", in); err != nil {
		e.Stop()
		return nil, err
	}
	own = &owned{intent: in}
	e.owned[c.ID] = own
	if state == "" {
		e.last[key] = now
		e.lease = time.Time{} // A newer uncertain write must not inherit an old watchdog lease.
		deadline := time.UnixMilli(c.Issued + c.TTL)
		execctx, cancel := context.WithDeadline(ctx, deadline)
		e.cancelMu.Lock()
		e.activeCancel = cancel
		if e.stopped.Load() {
			cancel()
		}
		e.cancelMu.Unlock()
		state = "TIMEOUT"
		b := e.bindings[key]
		if !e.stopped.Load() && e.opts.Now().Before(deadline) && e.conditions(c, p) {
			// Refuse an already-applied sequence, including a stale PLC mailbox
			// left behind when an operator commissions a fresh audit directory.
			actual, _, previous, readErr := e.opts.PLC.Readback(execctx, b, p.Mailbox)
			err = readErr
			if err == nil && previous == sequence {
				err = fmt.Errorf("request sequence already applied")
			}
			if err == nil && (math.Abs(c.Value-actual) > p.Delta || actual < p.Min || actual > p.Max) {
				err = fmt.Errorf("PLC active value violates slew/envelope")
				failGate(3, "PLC preflight value refused")
				state = "REJECTED_SAFETY_BOUNDS"
			}
			if err == nil {
				before = actual
				err = e.audit.Append("mailbox_preflight", map[string]any{"command_id": c.ID, "pre_write_value": actual, "previous_sequence": previous, "request_sequence": sequence})
				if err != nil {
					e.Stop()
				}
			}
			e.counter++
			if err == nil {
				err = e.opts.PLC.Heartbeat(execctx, b, p.Mailbox, e.counter)
			}
			if err == nil {
				err = e.opts.PLC.Stage(execctx, b, p.Mailbox, sequence, c.Value)
			}
			verifyctx, done := context.WithTimeout(execctx, e.opts.Verification)
			for err == nil && verifyctx.Err() == nil {
				value, reject, ack, readErr := e.opts.PLC.Readback(verifyctx, b, p.Mailbox)
				if readErr != nil {
					break
				}
				if verifyctx.Err() != nil || e.stopped.Load() || !e.opts.Now().Before(deadline) {
					break
				}
				if ack == sequence {
					after = value
					if reject != 0 {
						state = "PLC_INTERLOCK_REFUSED"
					} else if value == c.Value {
						state = "EXECUTED"
					}
					break
				}
				select {
				case <-verifyctx.Done():
				case <-time.After(10 * time.Millisecond):
				}
			}
			done()
			if state == "EXECUTED" {
				e.lease = deadline
				e.leaseKey = key
				e.leaseConditions = append([]Condition(nil), c.Conditions...)
			}
		}
		cancel()
		e.cancelMu.Lock()
		e.activeCancel = nil
		e.cancelMu.Unlock()
	}
	receipt := map[string]any{"schema_version": "1.0", "command_id": c.ID, "command_sha256": digest, "asset": c.Asset, "target_parameter": c.Parameter, "policy_revision": c.Revision, "recorded_at_ms": e.opts.Now().UnixMilli(), "state": state, "terminal": state != "AWAITING_OPERATOR_CONFIRMATION", "pre_write_value": before, "post_write_value": after, "unit": c.Unit, "gates": gates, "signature": map[string]any{"algorithm": "Ed25519", "canonicalization": "RFC8785", "key_id": e.opts.SigningKeyID, "value": ""}}
	encoded, _ := json.Marshal(receipt)
	signed, err := Sign(encoded, e.opts.SigningKey, true)
	if err != nil {
		return nil, err
	}
	if _, err = e.opts.Config.Validator.Validate("command-receipt", signed); err != nil {
		e.Stop()
		return nil, err
	}
	if err = e.audit.Append("receipt", json.RawMessage(signed)); err != nil {
		e.Stop()
		return nil, err
	}
	own.Receipt = signed
	own.Pending = state == "AWAITING_OPERATOR_CONFIRMATION"
	return append([]byte(nil), signed...), nil
}
func (e *Engine) RunWatchdog(ctx context.Context) error {
	ticker := time.NewTicker(e.opts.Heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			e.mu.Lock()
			if e.stopped.Load() || !e.opts.Now().Before(e.lease) {
				e.mu.Unlock()
				continue
			}
			parts := strings.Split(e.leaseKey, "/")
			_, p, ok := e.parameter(parts[0], parts[1])
			if !ok {
				e.mu.Unlock()
				continue
			}
			c := Command{Asset: parts[0], Conditions: e.leaseConditions}
			if !e.conditions(c, p) {
				e.lease = time.Time{}
				e.mu.Unlock()
				continue
			}
			b := e.bindings[e.leaseKey]
			e.counter++
			err := e.audit.Append("heartbeat", map[string]any{"asset": b.Asset, "parameter": b.Parameter, "counter": e.counter, "lease_until": e.lease})
			if err == nil {
				hctx, cancel := context.WithDeadline(ctx, minDeadline(time.Now().Add(e.opts.Heartbeat), e.lease))
				e.cancelMu.Lock()
				e.activeCancel = cancel
				if e.stopped.Load() {
					cancel()
				}
				e.cancelMu.Unlock()
				if !e.stopped.Load() && e.opts.Now().Before(e.lease) && e.conditions(c, p) {
					err = e.opts.PLC.Heartbeat(hctx, b, p.Mailbox, e.counter)
				}
				cancel()
				e.cancelMu.Lock()
				e.activeCancel = nil
				e.cancelMu.Unlock()
			}
			e.mu.Unlock()
			if err != nil {
				e.Stop()
				return err
			}
		}
	}
}
func (e *Engine) PendingReceipts() map[string][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string][]byte{}
	for _, r := range e.audit.Records {
		if r.Kind != "receipt" {
			continue
		}
		h := fmt.Sprintf("%x", sha256.Sum256(r.Body))
		if !e.sent[h] {
			out[h] = append([]byte(nil), r.Body...)
		}
	}
	return out
}
func (e *Engine) Published(hash string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sent[hash] {
		return nil
	}
	if err := e.audit.Append("published", hash); err != nil {
		return err
	}
	e.sent[hash] = true
	return nil
}
func (e *Engine) Stats() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	pending := 0
	for _, o := range e.owned {
		if o.Pending {
			pending++
		}
	}
	return map[string]any{"type": "actuator_health", "shadow_locked": e.stopped.Load(), "mqtt_ready": e.mqttReady.Load(), "commands": len(e.owned), "operator_pending": pending, "audit_records": len(e.audit.Records), "request_sequence": e.sequence, "watchdog_lease_until": e.lease}
}

func minDeadline(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
