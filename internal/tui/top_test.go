package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openump/Hades/internal/inspect"
)

var t0 = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// snapshot is served by a real inspect handler, so the TUI is tested against
// the inspector's actual JSON, not a hand-written copy of it.
func inspectorServer(t *testing.T) (*httptest.Server, *inspect.Model) {
	t.Helper()
	m := inspect.NewModel(func() time.Time { return t0.Add(2 * time.Second) })
	m.Declare("urn:openump:acme:berlin:press-04", "openump/v1/acme/berlin/assembly/line-01/press-04", map[string]time.Duration{"motor_temperature": time.Second, "spindle_speed": time.Second})
	m.Telemetry(map[string]any{"asset_id": "urn:openump:acme:berlin:press-04", "observed_at": t0.Format("2006-01-02T15:04:05.000Z"),
		"measurements": map[string]any{"motor_temperature": map[string]any{"value": 71.25, "unit": "Cel", "quality": "good"}, "spindle_speed": map[string]any{"value": 1200.0, "unit": "1/min", "quality": "uncertain"}}})
	gates, _ := json.Marshal([]map[string]any{{"gate": 1, "result": "PASS"}, {"gate": 2, "result": "PASS"}, {"gate": 3, "result": "FAIL"}, {"gate": 4, "result": "NOT_EVALUATED"}, {"gate": 5, "result": "NOT_EVALUATED"}})
	m.Receipt(inspect.Receipt{CommandID: "019a1234-5678-7000-8000-000000000391", Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", State: "REJECTED_SAFETY_BOUNDS", Terminal: true, Gates: gates, RecordedAt: t0})
	m.SetQuarantine(map[string]any{"quarantine_records": 3.0, "quarantine_bytes": 2048.0, "quarantine_evictions": 0.0, "rejected": 5.0, "storage_failures": 0.0, "disk_reserve_ok": true})
	m.SetCertificates([]inspect.Certificate{{Path: "/etc/hades/pki/gateway.crt", Subject: "gateway-press-04", NotAfter: t0.AddDate(0, 0, 12), DaysLeft: 12}})
	m.SetServices(map[string]any{"/etc/hades/normalizer.json": map[string]any{"revision": "r7", "ready": true, "digest_verified": true, "child_pid": float64(os.Getpid()), "started_at": t0.Add(-90 * time.Minute)},
		"/etc/hades/transfer.json": map[string]any{"revision": "r7", "ready": false, "digest_verified": false, "error": "live revision mismatch"},
		"/etc/hades/history.json":  map[string]any{"revision": "r7", "ready": false, "error": "dial unix live.sock: connect: no such file"}})
	srv := httptest.NewServer(inspect.NewHandler(m, inspect.NewBroadcaster(), time.Second))
	t.Cleanup(srv.Close)
	return srv, m
}

func TestOnceRendersEveryPaneDeterministically(t *testing.T) {
	srv, _ := inspectorServer(t)
	opts := Options{Endpoint: SnapshotURL(srv.URL), Now: func() time.Time { return t0.Add(3 * time.Second) }, ProcRoot: t.TempDir()}
	render := func() string {
		s := TakeOnce(context.Background(), opts, 0)
		if s.Err != nil {
			t.Fatal(s.Err)
		}
		return Once(s, t0.Add(3*time.Second), false)
	}
	out := render()
	if render() != out {
		t.Fatal("--once must be deterministic for the same snapshot")
	}
	for _, want := range []string{
		"normalizer", "verified", "1h30m3s", "transfer", "MISMATCH", "live revision mismatch", "unknown", // services, digest state, uptime
		"openump/v1/acme/berlin/assembly/line-01/press-04", "motor_temperature", "71.25", "Cel", "GOOD", "UNCERTAIN", // UNS tags
		"quarantine records 3", "rejected 5", "disk reserve ok", "gateway-press-04", "12", // quarantine and certificates
		"REJECTED_SAFETY_BOUNDS", "pressure_setpoint", "✓✓✗··", // 5-gate receipts
	} {
		if !strings.Contains(out, want) {
			t.Errorf("--once output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatal("--once output for pipes must carry no escape sequences")
	}
}

func TestKeyboardNavigationTransitionsState(t *testing.T) {
	s := &State{}
	for _, step := range []struct {
		key   string
		check func() bool
	}{
		{"tab", func() bool { return s.Focus == PaneTelemetry }},
		{"tab", func() bool { return s.Focus == PaneStorage }},
		{"shift+tab", func() bool { return s.Focus == PaneTelemetry }},
		{"4", func() bool { return s.Focus == PaneActuator }},
		{"tab", func() bool { return s.Focus == PaneServices }}, // wraps
		{"j", func() bool { return s.Scroll[PaneServices] == 1 }},
		{"k", func() bool { return s.Scroll[PaneServices] == 0 }},
		{"k", func() bool { return s.Scroll[PaneServices] == 0 }}, // never negative
		{"/", func() bool { return s.Filtering && s.Focus == PaneTelemetry }},
		{"s", func() bool { return s.Filter == "s" }},
		{"q", func() bool { return s.Filter == "sq" && !s.Quit }}, // q is text while filtering
		{"backspace", func() bool { return s.Filter == "s" }},
		{"enter", func() bool { return !s.Filtering && s.Filter == "s" }},
		{"esc", func() bool { return s.Filter == "" }},
		{"q", func() bool { return s.Quit }},
	} {
		s.Key(step.key)
		if !step.check() {
			t.Fatalf("after %q: %+v", step.key, s)
		}
	}
	s = &State{}
	s.Key("/")
	s.Key("ctrl+c")
	if !s.Quit {
		t.Fatal("Ctrl+C must quit even while filtering")
	}
}

func TestFilterAndFrameFitTheTerminal(t *testing.T) {
	srv, _ := inspectorServer(t)
	s := TakeOnce(context.Background(), Options{Endpoint: SnapshotURL(srv.URL), Now: func() time.Time { return t0.Add(3 * time.Second) }}, 0)
	for _, k := range []string{"/", "s", "p", "i", "enter"} {
		s.Key(k)
	}
	frame := strings.Join(Frame(s, 60, 12, t0.Add(3*time.Second), true), "\n")
	if !strings.Contains(frame, "spindle") || strings.Contains(frame, "motor_temperature") {
		t.Fatalf("filter 'spi' must keep only matching tags:\n%s", frame)
	}
	lines := Frame(s, 60, 12, t0, true)
	if len(lines) != 12 {
		t.Fatal("a frame must fill exactly the terminal height:", len(lines))
	}
	for _, l := range lines {
		if visible(l) > 60 {
			t.Fatalf("line wider than the terminal: %q", l)
		}
	}
	for i := 0; i < 50; i++ {
		s.Key("j") // scrolling past the end clamps
	}
	Frame(s, 60, 12, t0, true)
	if s.Scroll[PaneTelemetry] > 6 {
		t.Fatal("scroll must clamp to content:", s.Scroll[PaneTelemetry])
	}
}

// TestDaemonUnreachableAndRecovery runs the interactive loop: the inspector
// goes away (banner) and comes back on the same address (banner clears).
func TestDaemonUnreachableAndRecovery(t *testing.T) {
	srv, _ := inspectorServer(t)
	handler := srv.Config.Handler
	addr := srv.Listener.Addr().String()
	srv.Close()

	var mu sync.Mutex
	var buf bytes.Buffer
	out := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) })
	frames := func() string { mu.Lock(); defer mu.Unlock(); return buf.String() }
	reset := func() { mu.Lock(); buf.Reset(); mu.Unlock() }

	ctx, cancel := context.WithCancel(context.Background())
	keys := make(chan string)
	done := make(chan error, 1)
	before := runtime.NumGoroutine()
	go func() {
		done <- Run(ctx, Options{Endpoint: SnapshotURL(addr), Interval: 50 * time.Millisecond, Client: &http.Client{Timeout: 200 * time.Millisecond}}, keys, out, func() (int, int) { return 120, 40 })
	}()
	waitFor(t, func() bool { return strings.Contains(frames(), "DAEMON UNREACHABLE") }, "unreachable banner")

	l, err := net.Listen("tcp", addr) // the daemon restarts on its address
	if err != nil {
		t.Skip("address reuse unavailable:", err)
	}
	restarted := &http.Server{Handler: handler}
	go restarted.Serve(l)
	defer restarted.Close()
	reset()
	waitFor(t, func() bool {
		f := frames()
		i := strings.LastIndex(f, "\x1b[H")
		return i >= 0 && strings.Contains(f[i:], "normalizer") && !strings.Contains(f[i:], "DAEMON UNREACHABLE")
	}, "recovery")

	keys <- "q"
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel()
	restarted.Close()
	waitFor(t, func() bool { return runtime.NumGoroutine() <= before+1 }, "goroutines to exit")
}

func TestQuitAndSignalEndRunCleanly(t *testing.T) {
	srv, _ := inspectorServer(t)
	for _, how := range []string{"q", "ctrl+c", "sigterm"} {
		ctx, cancel := context.WithCancel(context.Background())
		keys := make(chan string, 1)
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Endpoint: SnapshotURL(srv.URL), Interval: 20 * time.Millisecond}, keys, ioDiscard{}, func() (int, int) { return 80, 24 })
		}()
		time.Sleep(60 * time.Millisecond)
		if how == "sigterm" {
			cancel() // hades-ctl maps SIGTERM to context cancellation
		} else {
			keys <- how
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(how, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal(how, "did not end Run")
		}
		cancel()
	}
}

func TestOnlyGETReachesTheInspector(t *testing.T) {
	var methods []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Write([]byte(`{"assets":[],"receipts":[],"services":{}}`))
	}))
	defer srv.Close()
	s := &State{}
	for _, k := range []string{"1", "2", "3", "4", "tab", "j", "k", "/", "x", "enter", "esc", "y", "n", "\r"} {
		s.Key(k)
	}
	s.Apply(Take(context.Background(), Options{Endpoint: SnapshotURL(srv.URL)}, nil))
	s.Apply(Take(context.Background(), Options{Endpoint: SnapshotURL(srv.URL)}, nil))
	for _, m := range methods {
		if m != "GET /ui/api/snapshot" {
			t.Fatal("hades top must only GET the snapshot, saw", m)
		}
	}
	if len(methods) != 2 {
		t.Fatal(methods)
	}
}

func TestOversizedOrInvalidSnapshotIsAnError(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte(" "), MaxSnapshotBytes+10))
	}))
	defer big.Close()
	if _, err := Fetch(context.Background(), http.DefaultClient, big.URL); err == nil {
		t.Fatal("an oversized snapshot must be refused")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(421) }))
	defer bad.Close()
	if _, err := Fetch(context.Background(), http.DefaultClient, bad.URL); err == nil || !strings.Contains(err.Error(), "421") {
		t.Fatal("HTTP errors must surface:", err)
	}
}

// chunks delivers each string as a separate Read, with a pause, like
// packets over SSH.
type chunks struct {
	parts []string
	gap   time.Duration
}

func (c *chunks) Read(p []byte) (int, error) {
	if len(c.parts) == 0 {
		return 0, io.EOF
	}
	time.Sleep(c.gap)
	n := copy(p, c.parts[0])
	c.parts = c.parts[1:]
	return n, nil
}

func decodeAll(t *testing.T, r io.Reader) string {
	t.Helper()
	keys := make(chan string, 32)
	ReadKeys(context.Background(), r, keys)
	close(keys)
	var got []string
	for k := range keys {
		got = append(got, k)
	}
	return strings.Join(got, " ")
}

func TestKeyDecoding(t *testing.T) {
	if got, want := decodeAll(t, strings.NewReader("\t\x1b[Z\x1b[A\x1b[Bj/\x7f\r\x03q")), "tab shift+tab up down j / backspace enter ctrl+c q"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// An arrow split across SSH packets is still one arrow, not Esc + "[A".
	if got := decodeAll(t, &chunks{parts: []string{"\x1b", "[A"}, gap: 10 * time.Millisecond}); got != "up" {
		t.Fatalf("split arrow decoded as %q", got)
	}
	// Esc then q in one read: both keys survive.
	if got := decodeAll(t, strings.NewReader("\x1bq")); got != "esc q" {
		t.Fatalf("Esc+q decoded as %q", got)
	}
	// A lone Esc, then a key in a later packet after the wait, are two keys.
	if got := decodeAll(t, &chunks{parts: []string{"\x1b", "q"}, gap: 2 * escWait}); got != "esc q" {
		t.Fatalf("lone Esc decoded as %q", got)
	}
}

func TestUntrustedTextCannotDriveTheTerminal(t *testing.T) {
	evil := "\x1b]52;c;cHduZWQ=\x07\x1b[2J\u009b31m\nrow\u202edaolyap\u202c\u2066x\u2069\u200f"
	snap := &inspect.Snapshot{
		Assets:       []inspect.Asset{{Topic: "openump/v1/" + evil, Signals: map[string]inspect.Signal{evil: {Value: evil, Unit: evil, Quality: evil, ObservedAt: t0}}}},
		Receipts:     []inspect.Receipt{{CommandID: evil, Asset: evil, Parameter: evil, State: evil, RecordedAt: t0}},
		Quarantine:   map[string]any{"error": evil},
		Certificates: []inspect.Certificate{{Path: "/x/" + evil, Subject: evil, NotAfter: t0}},
		Services:     map[string]any{"/etc/" + evil: map[string]any{"revision": evil, "error": evil}},
	}
	s := &State{}
	s.Apply(Sample{Snapshot: snap, At: t0})
	s.Err = errors.New(evil)
	out := Once(s, t0, false)
	for _, bad := range []string{"\x1b", "\x07", "\u009b", "\u202e", "\u2066", "\u200f"} {
		if strings.Contains(out, bad) {
			t.Fatalf("control character %q reached the terminal:\n%q", bad, out)
		}
	}
	if strings.Contains(out, "\nrow") {
		t.Fatal("an embedded newline must not break the row layout")
	}
	for _, l := range Frame(s, 80, 20, t0, true) {
		if visible(l) > 80 {
			t.Fatalf("sanitized line still wider than the terminal: %q", l)
		}
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hit bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer elsewhere.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusFound)
	}))
	defer redirect.Close()
	if _, err := Fetch(context.Background(), NewClient(time.Second), redirect.URL); err == nil || hit {
		t.Fatalf("a redirect must not be followed (err %v, followed %v)", err, hit)
	}
}

func TestCPUIsUnknownUntilTwoSamples(t *testing.T) {
	root := t.TempDir()
	pid := 4242
	dir := filepath.Join(root, "4242")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "statm"), []byte("5000 2560 100 1 0 300 0\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "stat"), []byte("4242 (x) S 1 1 1 0 -1 0 0 0 0 0 100 50 0 0 20 0 8 0 1 0\n"), 0o644)
	snap := &inspect.Snapshot{Services: map[string]any{"/etc/hades/x.json": map[string]any{"ready": true, "digest_verified": true, "child_pid": float64(pid)}}}
	s := &State{}
	s.Apply(Sample{Snapshot: snap, At: t0, Resources: ReadResources(root, PIDs(snap), nil, t0)})
	if out := Once(s, t0, false); !strings.Contains(out, "n/a") || strings.Contains(out, "0.0%") {
		t.Fatalf("a first sample must show CPU n/a, not 0.0%%:\n%s", out)
	}
}

func TestResizeRepaintsImmediately(t *testing.T) {
	srv, _ := inspectorServer(t)
	resize := make(chan struct{}, 1)
	var mu sync.Mutex
	width := 80
	var frames int
	out := writerFunc(func(p []byte) (int, error) { mu.Lock(); frames++; mu.Unlock(); return len(p), nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Endpoint: SnapshotURL(srv.URL), Interval: time.Hour, Resize: resize}, nil, out, func() (int, int) { mu.Lock(); defer mu.Unlock(); return width, 24 })
	}()
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return frames >= 2 }, "first frames")
	mu.Lock()
	before := frames
	width = 120
	mu.Unlock()
	resize <- struct{}{}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return frames > before }, "repaint on resize despite a 1h interval")
	cancel()
	<-done
}

func TestProcResources(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "42")
	os.MkdirAll(dir, 0o755)
	write := func(ticks string) {
		os.WriteFile(filepath.Join(dir, "statm"), []byte("5000 2560 100 1 0 300 0\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "stat"), []byte("42 (hades normal) S 1 42 42 0 -1 4194560 100 0 0 0 "+ticks+" 0 0 20 0 8 0 1 0\n"), 0o644)
	}
	write("100 50")
	first := ReadResources(root, []int{42, 99}, nil, t0)
	if _, ok := first[99]; ok || first[42].RSSBytes != 2560*int64(os.Getpagesize()) {
		t.Fatalf("%+v", first)
	}
	write("150 100") // 100 more ticks = 1 CPU-second over 2 s
	second := ReadResources(root, []int{42}, first, t0.Add(2*time.Second))
	if got := second[42].CPUPercent; got < 49.9 || got > 50.1 {
		t.Fatal("CPU share of one core:", got)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for", what)
		}
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
