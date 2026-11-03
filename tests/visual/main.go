// Command visual captures the Hades operator views from deterministic
// fixtures and compares them with reviewed baselines (Hades-044).
//
//	go run ./tests/visual -browser container -out artifacts/visual            # CI
//	go run ./tests/visual -browser host -out /tmp/visual -compare=false       # local preview
//	go run ./tests/visual -browser container -out artifacts/visual -refresh   # accept new baselines
//	go run ./tests/visual ... -vmui http://127.0.0.1:8428                     # also capture the history chart
//
// Baselines are only trusted from the pinned container browser on linux/amd64
// (the CI runner); other browsers render fonts differently and are for preview.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openump/Hades/internal/inspect"
	"github.com/openump/Hades/internal/tui"
)

// chromium is the pinned headless browser image used for baselines.
const chromium = "chromedp/headless-shell@sha256:7bee201478b2f6266aae01243740ee85c16281e32f42d3aed9fdf04f4eace7a1"

// target is one capture.
type target struct {
	Name          string
	Path          string // under the fixture server
	Width, Height int
	Ready         string // JavaScript readiness condition
	Compare       bool   // false: artifact with content checks only
}

// consoleReady: connected, and every section has rendered its table or its
// explicit empty note.
const consoleReady = `document.getElementById('connection').textContent === 'live' && ['uns', 'receipts', 'certificates', 'services'].every(id => document.getElementById(id).childElementCount > 0)`
const terminalReady = `document.getElementById('terminal') !== null`

func main() {
	browser := flag.String("browser", "container", "container (pinned Chromium, baselines) or host (local Chrome, preview)")
	out := flag.String("out", "artifacts/visual", "directory for captures and diffs")
	baselines := flag.String("baselines", "tests/visual/baselines", "reviewed baseline directory")
	compare := flag.Bool("compare", true, "compare with baselines (false for preview)")
	refresh := flag.Bool("refresh", false, "accept the captures as the new baselines")
	threshold := flag.Float64("threshold", 0.0005, "maximum fraction of differing pixels")
	vmui := flag.String("vmui", "", "optional VictoriaMetrics base URL (demo stack) to capture the history chart")
	chrome := flag.String("chrome", "", "host Chrome binary (-browser host; default: Chrome on macOS, google-chrome or chromium on Linux)")
	flag.Parse()
	if err := run(*browser, *out, *baselines, *compare, *refresh, *threshold, *vmui, *chrome); err != nil {
		fmt.Fprintln(os.Stderr, "visual:", err)
		os.Exit(1)
	}
}

func run(browser, out, baselines string, compare, refresh bool, threshold float64, vmui, chrome string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	server, err := fixtureServer()
	if err != nil {
		return err
	}
	debug, stop, err := startBrowser(browser, chrome)
	if err != nil {
		return err
	}
	defer stop()

	targets := []target{
		{Name: "console-desktop", Path: "/normal/ui/", Width: 1440, Height: 900, Ready: consoleReady, Compare: true},
		{Name: "console-narrow", Path: "/normal/ui/", Width: 390, Height: 844, Ready: consoleReady, Compare: true},
		{Name: "console-hostile", Path: "/hostile/ui/", Width: 1440, Height: 900, Ready: consoleReady, Compare: true},
	}
	for _, t := range terminalTargets() {
		targets = append(targets, target{Name: t, Path: "/terminal/" + t + ".html", Width: 1200, Height: 400, Ready: terminalReady, Compare: true})
	}
	masks, err := loadMasks(filepath.Join(baselines, "masks.json"))
	if err != nil {
		return err
	}
	var failures []string
	for _, t := range targets {
		p, err := newPage(debug)
		if err != nil {
			return err
		}
		png, err := p.capture(server+t.Path, t.Width, t.Height, t.Ready)
		p.close()
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		actual := filepath.Join(out, t.Name+".png")
		if err := os.WriteFile(actual, png, 0o644); err != nil {
			return err
		}
		if refresh {
			if err := os.MkdirAll(baselines, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(baselines, t.Name+".png"), png, 0o644); err != nil {
				return err
			}
			fmt.Printf("%-18s baseline refreshed\n", t.Name)
			continue
		}
		if !compare {
			fmt.Printf("%-18s captured (preview, not compared)\n", t.Name)
			continue
		}
		if msg := compareOne(t.Name, actual, filepath.Join(baselines, t.Name+".png"), filepath.Join(out, t.Name+".diff.png"), masks[t.Name], threshold); msg != "" {
			failures = append(failures, msg)
		}
	}
	if vmui != "" {
		if err := captureVMUI(debug, vmui, out); err != nil {
			failures = append(failures, "vmui: "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d visual check(s) failed:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
	return nil
}

// compareOne checks one capture and reports a failure message, or "".
func compareOne(name, actualPath, baselinePath, diffPath string, masks []Rect, threshold float64) string {
	actual, err := readPNG(actualPath)
	if err != nil {
		return name + ": " + err.Error()
	}
	baseline, err := readPNG(baselinePath)
	if err != nil {
		return fmt.Sprintf("%s: no reviewed baseline (%v); review the capture and run with -refresh", name, err)
	}
	c, diff := Compare(baseline, actual, masks, 16)
	if err := writePNG(diffPath, diff); err != nil {
		return fmt.Sprintf("%s: writing the diff: %v", name, err)
	}
	switch {
	case c.SizeMismatch:
		return fmt.Sprintf("%s: size changed from %v to %v (layout changed)", name, baseline.Bounds().Size(), actual.Bounds().Size())
	case c.Ratio() > threshold:
		return fmt.Sprintf("%s: %d of %d pixels differ (%.3f%%, limit %.3f%%); see %s", name, c.Different, c.Pixels, 100*c.Ratio(), 100*threshold, diffPath)
	}
	fmt.Printf("%-18s matches baseline (%d differing pixels)\n", name, c.Different)
	return ""
}

func loadMasks(path string) (map[string][]Rect, error) {
	masks := map[string][]Rect{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return masks, nil
	}
	if err != nil {
		return nil, err
	}
	return masks, json.Unmarshal(raw, &masks)
}

// terminalTargets lists the hades top frames rendered by the fixture server.
func terminalTargets() []string {
	return []string{"top-once", "top-pane-1", "top-pane-2", "top-pane-3", "top-pane-4", "top-filter", "top-hostile"}
}

// terminalFrames renders hades top with its real renderer at the fixed clock.
func terminalFrames() map[string]string {
	frames := map[string]string{}
	frames["top-once"] = TerminalHTML("hades top -once -color", tui.Once(topState(normalModel()), clock, true))
	for pane := 0; pane < 4; pane++ {
		s := topState(normalModel())
		for i := 0; i < pane; i++ {
			s.Key("tab")
		}
		frames[fmt.Sprintf("top-pane-%d", pane+1)] = TerminalHTML("hades top", strings.Join(tui.Frame(s, 110, 24, clock, true), "\n"))
	}
	s := topState(normalModel())
	for _, k := range []string{"/", "s", "p", "i"} {
		s.Key(k)
	}
	frames["top-filter"] = TerminalHTML("hades top  (filtering)", strings.Join(tui.Frame(s, 110, 24, clock, true), "\n"))
	frames["top-hostile"] = TerminalHTML("hades top -once -color  (hostile content)", tui.Once(topState(hostileModel()), clock, true))
	return frames
}

// fixtureServer serves the real console for the normal and hostile models
// and the rendered terminal frames. It binds loopback; the container browser
// shares the host network on Linux.
func fixtureServer() (string, error) {
	mux := http.NewServeMux()
	for prefix, model := range map[string]*inspect.Model{"/normal": normalModel(), "/hostile": hostileModel()} {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, inspect.NewHandler(model, inspect.NewBroadcaster(), time.Hour)))
	}
	frames := terminalFrames()
	mux.HandleFunc("/terminal/", func(w http.ResponseWriter, r *http.Request) {
		page, ok := frames[strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/terminal/"), ".html")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go http.Serve(l, mux)
	return "http://" + l.Addr().String(), nil
}

// startBrowser starts a headless browser with a DevTools port and returns
// its address and a stop function.
func startBrowser(kind, chrome string) (string, func(), error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	debug := fmt.Sprintf("127.0.0.1:%d", port)
	var cmd *exec.Cmd
	var stop func()
	switch kind {
	case "container":
		// The image's entrypoint serves DevTools on port 9222; with the host
		// network (Linux CI) it is reachable on loopback. Refuse if anything
		// already listens there: captures must come from the pinned browser.
		debug = "127.0.0.1:9222"
		if c, err := net.DialTimeout("tcp", debug, time.Second); err == nil {
			c.Close()
			return "", nil, fmt.Errorf("port 9222 is already in use; the pinned browser needs it")
		}
		// Pull first, so a cold image download does not eat the start deadline.
		if out, err := exec.Command("docker", "pull", "--platform", "linux/amd64", chromium).CombinedOutput(); err != nil {
			return "", nil, fmt.Errorf("pulling %s: %v: %s", chromium, err, out)
		}
		name := fmt.Sprintf("hades-visual-%d", os.Getpid())
		cmd = exec.Command("docker", "run", "--rm", "--name", name, "--network", "host", "--platform", "linux/amd64", chromium, "--hide-scrollbars")
		stop = func() { exec.Command("docker", "stop", "-t", "2", name).Run(); cmd.Wait() }
	case "host":
		if chrome == "" {
			chrome = hostChrome()
		}
		dir, err := os.MkdirTemp("", "hades-visual-chrome-")
		if err != nil {
			return "", nil, err
		}
		cmd = exec.Command(chrome, "--headless=new", "--disable-gpu", "--hide-scrollbars", "--no-first-run", "--disable-extensions",
			"--user-data-dir="+dir, fmt.Sprintf("--remote-debugging-port=%d", port), "about:blank")
		stop = func() { cmd.Process.Kill(); cmd.Wait(); os.RemoveAll(dir) }
	default:
		return "", nil, fmt.Errorf("unknown -browser %q", kind)
	}
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(250 * time.Millisecond) {
		if resp, err := http.Get("http://" + debug + "/json/version"); err == nil {
			resp.Body.Close()
			return debug, stop, nil
		}
		if time.Now().After(deadline) {
			stop()
			return "", nil, fmt.Errorf("%s browser did not open its DevTools port", kind)
		}
	}
}

// hostChrome finds a local Chrome for previews.
func hostChrome() string {
	for _, c := range []string{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return "google-chrome"
}

// captureVMUI captures the history chart of a running demo stack and checks
// what rendered: the series, its labels and a drawn plot. It is not compared
// with a baseline, because the time axis moves with the data.
func captureVMUI(debug, base, out string) error {
	u := strings.TrimSuffix(base, "/") + "/vmui/?#/?g0.expr=" + url.QueryEscape("hades_observation_value") + "&g0.range_input=10m&g0.relative_time=last_10_minutes&g0.tab=0"
	p, err := newPage(debug)
	if err != nil {
		return err
	}
	defer p.close()
	// The legend reads "hades_observation_value { asset_id=... }".
	ready := `/hades_observation_value\s*\{/.test(document.body.innerText) && document.body.innerText.includes('press-04') && document.querySelector('canvas') !== null`
	png, err := p.capture(u, 1440, 900, ready)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "vmui-history.png"), png, 0o644); err != nil {
		return err
	}
	stats, err := p.evaluate(`(() => { const t = document.body.innerText; const m = t.match(/min:\s*([0-9.]+).*?max:\s*([0-9.]+)/s); return m ? m[1] + '..' + m[2] : ''; })()`)
	if err != nil || string(stats) == `""` {
		return fmt.Errorf("history chart rendered without series statistics")
	}
	fmt.Printf("%-18s rendered series hades_observation_value for press-04, range %s (content checked)\n", "vmui-history", strings.Trim(string(stats), `"`))
	return nil
}
