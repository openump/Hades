// Package inspect is a strictly read-only local commissioning console. It
// aggregates data that Hades already exposes read-only (broker telemetry,
// status and signed command receipts, normalizer health, PKI files and
// supervisor status) and never touches a control socket or PLC.
package inspect

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Bounds keep the console's memory small and fixed, however busy the plant is.
const (
	MaxAssets   = 64
	MaxReceipts = 32
)

// Signal is the latest value of one measurement.
type Signal struct {
	Value      any       `json:"value"`
	Unit       string    `json:"unit"`
	Quality    string    `json:"quality"` // GOOD, UNCERTAIN, BAD or STALE
	ObservedAt time.Time `json:"observed_at"`
}

// Asset is one UNS node: its topic prefix, latest signals and producer status.
type Asset struct {
	ID       string            `json:"asset_id"`
	Topic    string            `json:"topic"`
	Signals  map[string]Signal `json:"signals"`
	Status   json.RawMessage   `json:"status,omitempty"`
	interval map[string]time.Duration
}

// Receipt is one signed actuator receipt, with its gate results.
type Receipt struct {
	CommandID  string          `json:"command_id"`
	Asset      string          `json:"asset"`
	Parameter  string          `json:"target_parameter"`
	State      string          `json:"state"`
	Terminal   bool            `json:"terminal"`
	Gates      json.RawMessage `json:"gates"`
	RecordedAt time.Time       `json:"recorded_at"`
}

// Snapshot is the full read-only view served to the browser.
type Snapshot struct {
	GeneratedAt  time.Time      `json:"generated_at"`
	Assets       []Asset        `json:"assets"`
	Receipts     []Receipt      `json:"receipts"`
	Quarantine   map[string]any `json:"quarantine"`
	Certificates []Certificate  `json:"certificates"`
	Services     map[string]any `json:"services"`
}

// Certificate is a PKI file's identity and expiry.
type Certificate struct {
	Path     string    `json:"path"`
	Subject  string    `json:"subject"`
	NotAfter time.Time `json:"not_after"`
	DaysLeft int       `json:"days_left"`
}

// Model holds the bounded live state.
type Model struct {
	mu       sync.Mutex
	assets   map[string]*Asset
	receipts []Receipt // ring, newest last, at most MaxReceipts
	quar     map[string]any
	certs    []Certificate
	services map[string]any
	now      func() time.Time
	changed  func()
}

func NewModel(now func() time.Time) *Model {
	return &Model{assets: map[string]*Asset{}, quar: map[string]any{}, services: map[string]any{}, now: now, changed: func() {}}
}

// Declare registers an approved asset and the expected interval of each of its
// measurements, used to mark a silent signal STALE.
func (m *Model) Declare(id, topic string, intervals map[string]time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.assets) >= MaxAssets {
		return
	}
	m.assets[id] = &Asset{ID: id, Topic: topic, Signals: map[string]Signal{}, interval: intervals}
}

// Telemetry records a contract-validated telemetry payload for a declared asset.
func (m *Model) Telemetry(payload map[string]any) {
	id, _ := payload["asset_id"].(string)
	observed, _ := time.Parse("2006-01-02T15:04:05.000Z", stringOf(payload["observed_at"]))
	m.mu.Lock()
	a := m.assets[id]
	if a == nil {
		m.mu.Unlock()
		return
	}
	measurements, _ := payload["measurements"].(map[string]any)
	for name, raw := range measurements {
		if _, declared := a.interval[name]; !declared {
			continue // only declared measurements; keeps memory bounded
		}
		r, _ := raw.(map[string]any)
		if prev, seen := a.Signals[name]; seen && !observed.After(prev.ObservedAt) {
			continue // out-of-order or redelivered: never step back to an older reading
		}
		a.Signals[name] = Signal{Value: r["value"], Unit: stringOf(r["unit"]), Quality: strings.ToUpper(stringOf(r["quality"])), ObservedAt: observed}
	}
	notify := m.changed
	m.mu.Unlock()
	notify()
}

// Status records a contract-validated status payload.
func (m *Model) Status(id string, raw []byte) {
	m.mu.Lock()
	if a := m.assets[id]; a != nil {
		a.Status = append(json.RawMessage(nil), raw...)
	}
	notify := m.changed
	m.mu.Unlock()
	notify()
}

// Receipt records a verified receipt in the bounded ring.
func (m *Model) Receipt(r Receipt) {
	m.mu.Lock()
	for i := range m.receipts {
		if m.receipts[i].CommandID == r.CommandID {
			// One row per command: a later state or a redelivery replaces it.
			m.receipts = append(m.receipts[:i], m.receipts[i+1:]...)
			break
		}
	}
	m.receipts = append(m.receipts, r)
	if len(m.receipts) > MaxReceipts {
		m.receipts = append(m.receipts[:0], m.receipts[len(m.receipts)-MaxReceipts:]...)
	}
	notify := m.changed
	m.mu.Unlock()
	notify()
}

func (m *Model) SetQuarantine(stats map[string]any) {
	m.mu.Lock()
	if reflect.DeepEqual(m.quar, stats) {
		m.mu.Unlock()
		return
	}
	m.quar = stats
	notify := m.changed
	m.mu.Unlock()
	notify()
}

func (m *Model) SetCertificates(c []Certificate) {
	m.mu.Lock()
	if reflect.DeepEqual(m.certs, c) {
		m.mu.Unlock()
		return
	}
	m.certs = c
	notify := m.changed
	m.mu.Unlock()
	notify()
}

func (m *Model) SetServices(s map[string]any) {
	m.mu.Lock()
	if reflect.DeepEqual(m.services, s) {
		m.mu.Unlock()
		return
	}
	m.services = s
	notify := m.changed
	m.mu.Unlock()
	notify()
}

// Snapshot copies the current state. A signal silent for more than three
// expected intervals is reported STALE, whatever its last quality was.
func (m *Model) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := Snapshot{GeneratedAt: now, Quarantine: m.quar, Certificates: append([]Certificate(nil), m.certs...), Services: m.services}
	for _, a := range m.assets {
		copyA := Asset{ID: a.ID, Topic: a.Topic, Status: a.Status, Signals: map[string]Signal{}}
		for name, s := range a.Signals {
			if limit := 3 * a.interval[name]; limit > 0 && now.Sub(s.ObservedAt) > limit {
				s.Quality = "STALE"
			}
			copyA.Signals[name] = s
		}
		out.Assets = append(out.Assets, copyA)
	}
	sort.Slice(out.Assets, func(i, j int) bool { return out.Assets[i].ID < out.Assets[j].ID })
	out.Receipts = append([]Receipt(nil), m.receipts...)
	return out
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

// OnChange sets the function called after every model update (for example a
// Broadcaster's non-blocking Notify).
func (m *Model) OnChange(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.changed = f
}
