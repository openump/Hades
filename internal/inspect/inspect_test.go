package inspect

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, m *Model, b *Broadcaster) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(NewHandler(m, b, 50*time.Millisecond))
	t.Cleanup(ts.Close)
	return ts
}

// TestConsoleLoadsOfflineWithStrictCSP: every embedded asset is served, the CSP
// allows only 'self', and no asset references an external origin.
func TestConsoleLoadsOfflineWithStrictCSP(t *testing.T) {
	ts := testServer(t, NewModel(time.Now), NewBroadcaster())
	external := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}`)
	err := fs.WalkDir(webFiles, "web", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		resp, err := http.Get(ts.URL + "/ui/" + strings.TrimPrefix(path, "web/"))
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
		if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "http") {
			t.Errorf("%s: weak CSP %q", path, csp)
		}
		if m := external.FindString(string(body)); m != "" {
			t.Errorf("%s references an external origin: %s", path, m)
		}
		if strings.Contains(string(body), "<script>") || strings.Contains(string(body), "style=") || strings.Contains(string(body), "innerHTML") {
			t.Errorf("%s uses inline script/style or innerHTML", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEveryWriteVerbIsRejected: the console is GET/HEAD only on every route.
func TestEveryWriteVerbIsRejected(t *testing.T) {
	ts := testServer(t, NewModel(time.Now), NewBroadcaster())
	for _, path := range []string{"/ui", "/ui/", "/ui/api/snapshot", "/ui/stream", "/ui/app.js", "/ui/does-not-exist", "/"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "CONNECT", "TRACE"} {
			req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader("{}"))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
				t.Fatalf("%s %s: %d, want 405", method, path, resp.StatusCode)
			}
		}
	}
	if resp, _ := http.Head(ts.URL + "/ui/api/snapshot"); resp.StatusCode != http.StatusOK {
		t.Fatal("HEAD must be allowed", resp.StatusCode)
	}
}

func TestSnapshotSurfacesQualityStaleReceiptsAndDiagnostics(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	m := NewModel(func() time.Time { return now })
	m.Declare("urn:openump:acme:berlin:press-04", "openump/v1/acme/berlin/assembly/line-01/press-04", map[string]time.Duration{"motor_temperature": time.Second, "cycle_counter": time.Second})
	at := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02T15:04:05.000Z") }
	m.Telemetry(map[string]any{"asset_id": "urn:openump:acme:berlin:press-04", "observed_at": at(500 * time.Millisecond),
		"measurements": map[string]any{"motor_temperature": map[string]any{"value": 71.5, "unit": "Cel", "quality": "good"}, "undeclared": map[string]any{"value": 1, "unit": "1", "quality": "good"}}})
	m.Telemetry(map[string]any{"asset_id": "urn:openump:acme:berlin:press-04", "observed_at": at(10 * time.Second),
		"measurements": map[string]any{"cycle_counter": map[string]any{"value": 9, "unit": "1", "quality": "uncertain"}}})
	for i := 0; i < MaxReceipts+5; i++ {
		m.Receipt(Receipt{CommandID: fmt.Sprintf("cmd-%d", i), State: "SHADOW_LOGGED", Gates: json.RawMessage(`[{"gate":1,"result":"PASS"}]`)})
	}
	m.SetQuarantine(map[string]any{"quarantine_records": 3, "rejected": 7})
	s := m.Snapshot()
	sig := s.Assets[0].Signals
	if sig["motor_temperature"].Quality != "GOOD" || sig["cycle_counter"].Quality != "STALE" {
		t.Fatalf("expected GOOD and STALE (silent >3 intervals), got %+v", sig)
	}
	if _, kept := sig["undeclared"]; kept {
		t.Fatal("undeclared measurements must not be stored")
	}
	if len(s.Receipts) != MaxReceipts || s.Quarantine["quarantine_records"] != 3 {
		t.Fatal("receipt ring must stay bounded and diagnostics surfaced", len(s.Receipts), s.Quarantine)
	}
}

// TestSlowStreamNeverBlocksPublisherAndStreamsAreCapped.
func TestSlowStreamNeverBlocksPublisherAndStreamsAreCapped(t *testing.T) {
	m := NewModel(time.Now)
	b := NewBroadcaster()
	m.changed = b.Notify
	ts := testServer(t, m, b)
	var bodies []io.ReadCloser
	for i := 0; i < MaxStreams; i++ {
		resp, err := http.Get(ts.URL + "/ui/stream")
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatal(err, resp)
		}
		bodies = append(bodies, resp.Body) // never read: stalled clients
	}
	defer func() {
		for _, body := range bodies {
			body.Close()
		}
	}()
	for deadline := time.Now().Add(2 * time.Second); b.Active() < MaxStreams && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	if resp, _ := http.Get(ts.URL + "/ui/stream"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("a fifth stream must be refused", resp.StatusCode)
	}
	started := time.Now()
	for i := 0; i < 10000; i++ {
		m.SetQuarantine(map[string]any{"n": i}) // every change notifies all stalled streams
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("publishing blocked on stalled streams: %v", elapsed)
	}
	// A live reader still receives the latest snapshot.
	bodies[0].Close()
	bodies = bodies[1:]
	for deadline := time.Now().Add(2 * time.Second); b.Active() >= MaxStreams && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
	resp, err := http.Get(ts.URL + "/ui/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if !strings.HasPrefix(line, "event: snapshot") {
		t.Fatal("a stream must start with a snapshot:", line)
	}
}

// TestBoundedMemoryUnderLoad fills every bound (assets, signals, receipts)
// and holds the maximum streams, then measures heap growth.
func TestBoundedMemoryUnderLoad(t *testing.T) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	m := NewModel(time.Now)
	b := NewBroadcaster()
	m.OnChange(b.Notify)
	for a := 0; a < MaxAssets*2; a++ { // twice the cap: extra assets are dropped
		names := map[string]time.Duration{}
		for s := 0; s < 64; s++ {
			names[fmt.Sprintf("signal_%d", s)] = time.Second
		}
		m.Declare(fmt.Sprintf("urn:openump:acme:berlin:asset-%d", a), "t", names)
	}
	for i := 0; i < 2000; i++ {
		for a := 0; a < MaxAssets; a++ {
			ms := map[string]any{}
			for s := 0; s < 64; s++ {
				ms[fmt.Sprintf("signal_%d", s)] = map[string]any{"value": float64(i), "unit": "Cel", "quality": "good"}
			}
			m.Telemetry(map[string]any{"asset_id": fmt.Sprintf("urn:openump:acme:berlin:asset-%d", a), "observed_at": "2026-10-31T10:00:00.000Z", "measurements": ms})
		}
		m.Receipt(Receipt{CommandID: fmt.Sprintf("c-%d", i), Gates: json.RawMessage(`[{"gate":1,"result":"PASS"}]`)})
	}
	for i := 0; i < MaxStreams; i++ {
		b.Subscribe()
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	growth := float64(after.HeapAlloc) - float64(before.HeapAlloc)
	t.Logf("retained heap at full bounds (%d assets x 64 signals, %d receipts, %d streams): %.2f MB", MaxAssets, MaxReceipts, MaxStreams, growth/(1<<20))
	if growth > 2<<20 {
		t.Fatalf("console retained %.2f MB, over the 2 MB budget", growth/(1<<20))
	}
	if s := m.Snapshot(); len(s.Assets) != MaxAssets || len(s.Receipts) != MaxReceipts {
		t.Fatal("bounds not enforced", len(s.Assets), len(s.Receipts))
	}
}

// TestDNSRebindingHostIsRefused: a page that rebinds its own hostname to the
// loopback console is refused; real loopback hosts are allowed.
func TestDNSRebindingHostIsRefused(t *testing.T) {
	h := NewHandler(NewModel(time.Now), NewBroadcaster(), time.Second, LoopbackHosts("127.0.0.1:18091")...)
	for host, want := range map[string]int{
		"127.0.0.1:18091": http.StatusOK, "localhost:18091": http.StatusOK, "LOCALHOST:18091": http.StatusOK,
		"evil.example:18091": http.StatusMisdirectedRequest, "attacker.test": http.StatusMisdirectedRequest, "127.0.0.1:9999": http.StatusMisdirectedRequest,
	} {
		r := httptest.NewRequest(http.MethodGet, "/ui/api/snapshot", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %q: %d, want %d", host, w.Code, want)
		}
	}
}

// TestShutdownWithOpenStreamIsPromptAndClean: an open browser stream must not
// turn a normal stop into a timeout error.
func TestShutdownWithOpenStreamIsPromptAndClean(t *testing.T) {
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{Addr: addr, Handler: NewHandler(NewModel(time.Now), NewBroadcaster(), time.Hour, LoopbackHosts(addr)...)}
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv) }()
	var resp *http.Response
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var err error
		if resp, err = http.Get("http://" + addr + "/ui/stream"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
	defer resp.Body.Close()
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a normal stop with an open stream must not error: %v", err)
		}
		if time.Since(started) > time.Second {
			t.Fatalf("shutdown waited %v on the open stream", time.Since(started))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hung on an open stream")
	}
}

func TestOutOfOrderTelemetryAndReceiptDedupe(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	m := NewModel(func() time.Time { return now })
	m.Declare("a", "t", map[string]time.Duration{"temp": time.Hour})
	stamp := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02T15:04:05.000Z") }
	m.Telemetry(map[string]any{"asset_id": "a", "observed_at": stamp(time.Second), "measurements": map[string]any{"temp": map[string]any{"value": 72.0, "unit": "Cel", "quality": "good"}}})
	// A redelivered older reading must not step the display back.
	m.Telemetry(map[string]any{"asset_id": "a", "observed_at": stamp(10 * time.Second), "measurements": map[string]any{"temp": map[string]any{"value": 60.0, "unit": "Cel", "quality": "good"}}})
	if v := m.Snapshot().Assets[0].Signals["temp"].Value; v != 72.0 {
		t.Fatal("an out-of-order reading replaced the newer one:", v)
	}
	// One row per command; a later state replaces the earlier one.
	m.Receipt(Receipt{CommandID: "cmd-1", State: "AWAITING_OPERATOR_CONFIRMATION"})
	m.Receipt(Receipt{CommandID: "cmd-2", State: "SHADOW_LOGGED"})
	m.Receipt(Receipt{CommandID: "cmd-1", State: "EXECUTED"})
	m.Receipt(Receipt{CommandID: "cmd-1", State: "EXECUTED"}) // QoS1 redelivery
	r := m.Snapshot().Receipts
	if len(r) != 2 || r[len(r)-1].CommandID != "cmd-1" || r[len(r)-1].State != "EXECUTED" {
		t.Fatalf("expected one row per command with the latest state: %+v", r)
	}
}
