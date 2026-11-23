package main

// The standards-boundary suite (Hades-046): hades import, hades check and
// hades aas export, run from a committed storyboard against fixed inputs,
// captured as terminal and file pages, shown in an independent AAS viewer
// (Eclipse BaSyx), and assembled into a captioned demo video.

import (
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Video geometry: every frame is 1280x720.
const (
	videoW, videoH = 1280, 720
	captionH       = 64
)

// storyboard is tests/visual/aas/storyboard.json.
type storyboard struct {
	Inputs []string `json:"inputs"` // copied by base name into the work directory
	Steps  []step   `json:"steps"`
}

// step is one scene. Kinds:
//   - card: a title card (video only);
//   - terminal: a command typed and run in the work directory; its exit code
//     and output are checked;
//   - setup: a hidden command (sh -c), for example to derive a test input;
//   - files: generated files rendered side by side;
//   - viewer: a view of the AAS viewer, content-checked;
//   - reset: empty the AAS repository (hidden).
type step struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Caption   string   `json:"caption"`
	Title     string   `json:"title"`
	Subtitle  string   `json:"subtitle"`
	Run       []string `json:"run"`
	Shell     string   `json:"shell"`
	Exit      int      `json:"exit"`
	Expect    []string `json:"expect"`
	Panels    []panel  `json:"panels"`
	View      *view    `json:"view"`
	Live      bool     `json:"live"`  // varies run to run: content-checked, not baseline-compared
	Video     bool     `json:"video"` // part of the demo video
	Highlight bool     `json:"highlight"`
	Hold      float64  `json:"hold"` // seconds the final frame is shown
	// SameFirstField: every output line starts with the same field (for
	// example one digest printed for two files).
	SameFirstField bool `json:"same_first_field"`
}

type panel struct {
	Title   string   `json:"title"`
	Path    string   `json:"path"`
	Kind    string   `json:"kind"`    // json, csv or text
	Rows    []int    `json:"rows"`    // csv: highlighted data rows (1-based)
	Wide    bool     `json:"wide"`    // a full-width row above the others
	Columns int      `json:"columns"` // json: flow a long file into columns
	Fields  []string `json:"fields"`  // csv: the columns to show (default all)
}

// view addresses a BaSyx AAS viewer page: the shell, and optionally one
// submodel element by submodel id and idShort path.
type view struct {
	AAS      string `json:"aas"`
	Submodel string `json:"submodel"`
	Element  string `json:"element"`
}

// scene is a rendered step: what the page server shows and the video uses.
type scene struct {
	step
	command string // shell-quoted command line
	output  string
	image   string // viewer: captured PNG path
}

// aasConfig is the suite's command-line configuration.
type aasConfig struct {
	storyboard, work, bin, out, baselines string
	basyxAPI, basyxUI, liveSite           string
	compare, refresh, video               bool
	threshold                             float64
	ffmpeg, videoDir                      string
}

func runAAS(debug string, cfg aasConfig) error {
	raw, err := os.ReadFile(cfg.storyboard)
	if err != nil {
		return err
	}
	var sb storyboard
	if err := json.Unmarshal(raw, &sb); err != nil {
		return fmt.Errorf("storyboard: %w", err)
	}
	for _, st := range sb.Steps {
		for _, e := range st.Expect {
			if st.Kind == "terminal" {
				if _, err := regexp.Compile(e); err != nil {
					return fmt.Errorf("storyboard %s: %w", st.Name, err)
				}
			}
		}
	}
	if err := prepareWork(cfg, sb.Inputs); err != nil {
		return err
	}
	scenes := map[string]*scene{}
	var order []*scene
	var failures, visual []string // functional checks; baseline differences
	fail := func(format string, args ...any) { failures = append(failures, fmt.Sprintf(format, args...)) }
	pages := &pageServer{scenes: scenes, work: cfg.work}
	server, err := pages.start()
	if err != nil {
		return err
	}
	live := map[string]string{}
	for _, st := range sb.Steps {
		sc := &scene{step: st}
		switch st.Kind {
		case "setup":
			cmd := exec.Command("sh", "-c", st.Shell)
			cmd.Dir, cmd.Env = cfg.work, workEnv(cfg)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%s: setup failed: %v: %s", st.Name, err, out)
			}
			if strings.HasPrefix(st.Name, "live-values") {
				if err := readLiveValues(cfg.work, live); err != nil {
					return err
				}
			}
			continue
		case "reset":
			if err := basyxReset(cfg.basyxAPI); err != nil {
				return fmt.Errorf("%s: %w", st.Name, err)
			}
			continue
		case "terminal":
			sc.command = shellQuote(st.Run)
			out, code, err := runStep(cfg, st.Run)
			if err != nil {
				return fmt.Errorf("%s: %w", st.Name, err)
			}
			sc.output = out
			if code != st.Exit {
				fail("%s: exit %d, want %d; output:\n%s", st.Name, code, st.Exit, out)
			}
			for _, e := range st.Expect {
				re, err := regexp.Compile(substitute(e, live))
				if err != nil || !re.MatchString(out) {
					fail("%s: output does not match %q:\n%s", st.Name, e, out)
				}
			}
			if st.SameFirstField {
				var first []string
				for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
					if f := strings.Fields(line); len(f) > 0 {
						first = append(first, f[0])
					}
				}
				if len(first) < 2 || strings.Count(strings.Join(first, " "), first[0]) != len(first) {
					fail("%s: lines do not share their first field:\n%s", st.Name, out)
				}
			}
		case "files", "card":
		case "viewer":
			png, err := captureViewer(debug, cfg, st, live)
			if err != nil {
				fail("%s: %v", st.Name, err)
				continue
			}
			sc.image = filepath.Join(cfg.out, "aas-"+st.Name+".png")
			if err := os.WriteFile(sc.image, png, 0o644); err != nil {
				return err
			}
			fmt.Printf("%-28s rendered in the AAS viewer (content checked)\n", "aas-"+st.Name)
		default:
			return fmt.Errorf("%s: unknown kind %q", st.Name, st.Kind)
		}
		scenes[st.Name] = sc
		order = append(order, sc)
	}
	// Baseline captures: final terminal and file pages of deterministic steps.
	for _, sc := range order {
		if sc.Live || (sc.Kind != "terminal" && sc.Kind != "files") {
			continue
		}
		name := "aas-" + sc.Name
		p, err := newPage(debug)
		if err != nil {
			return err
		}
		png, err := p.capture(server+"/scene/"+sc.Name+"?mode=full", videoW, 300, "document.getElementById('scene') !== null")
		p.close()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		actual := filepath.Join(cfg.out, name+".png")
		if err := os.WriteFile(actual, png, 0o644); err != nil {
			return err
		}
		switch {
		case cfg.refresh:
			if err := os.WriteFile(filepath.Join(cfg.baselines, name+".png"), png, 0o644); err != nil {
				return err
			}
			fmt.Printf("%-28s baseline refreshed\n", name)
		case !cfg.compare:
			fmt.Printf("%-28s captured (preview, not compared)\n", name)
		default:
			if msg := compareOne(name, actual, filepath.Join(cfg.baselines, name+".png"), filepath.Join(cfg.out, name+".diff.png"), nil, cfg.threshold); msg != "" {
				visual = append(visual, msg)
			}
		}
	}
	for _, sc := range order {
		if sc.Live && sc.Kind == "terminal" {
			p, err := newPage(debug)
			if err != nil {
				return err
			}
			png, err := p.capture(server+"/scene/"+sc.Name+"?mode=full", videoW, 300, "document.getElementById('scene') !== null")
			p.close()
			if err == nil {
				err = os.WriteFile(filepath.Join(cfg.out, "aas-"+sc.Name+".png"), png, 0o644)
			}
			if err != nil {
				return err
			}
			fmt.Printf("%-28s captured (live values, content checked)\n", "aas-"+sc.Name)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d check(s) failed:\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
	// A baseline difference still lets the video build, so the run's
	// artifacts hold everything a reviewer needs.
	if cfg.video {
		if err := buildVideo(debug, server, order, cfg); err != nil {
			return fmt.Errorf("video: %w", err)
		}
	}
	if len(visual) > 0 {
		return fmt.Errorf("%d capture(s) differ from their baselines:\n  %s", len(visual), strings.Join(visual, "\n  "))
	}
	return nil
}

// prepareWork copies the inputs and the schemas into the work directory, so
// every command uses short relative paths and prints identical output on any
// machine.
func prepareWork(cfg aasConfig, inputs []string) error {
	if err := os.MkdirAll(cfg.work, 0o755); err != nil {
		return err
	}
	copyFile := func(src, dst string) error {
		raw, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o644)
	}
	for _, in := range inputs {
		if err := copyFile(in, filepath.Join(cfg.work, filepath.Base(in))); err != nil {
			return err
		}
	}
	if err := filepath.WalkDir("schemas", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return copyFile(path, filepath.Join(cfg.work, path))
	}); err != nil {
		return err
	}
	if cfg.liveSite != "" {
		return copyFile(filepath.Join(cfg.liveSite, "catalog", "metadata.json"), filepath.Join(cfg.work, "site", "catalog", "metadata.json"))
	}
	return nil
}

func workEnv(cfg aasConfig) []string {
	env := []string{"PATH=" + cfg.bin + string(os.PathListSeparator) + os.Getenv("PATH"), "TZ=UTC", "LC_ALL=C", "HOME=" + cfg.work,
		"BASYX=" + cfg.basyxAPI}
	return env
}

// runStep runs one command with stdout and stderr interleaved, as a
// terminal shows them.
func runStep(cfg aasConfig, argv []string) (string, int, error) {
	path, err := lookPath(cfg.bin, argv[0])
	if err != nil {
		return "", 0, err
	}
	cmd := exec.Command(path, argv[1:]...)
	cmd.Args[0] = argv[0]
	cmd.Dir, cmd.Env = cfg.work, workEnv(cfg)
	r, w, err := os.Pipe()
	if err != nil {
		return "", 0, err
	}
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		return "", 0, err
	}
	w.Close()
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	timer := time.AfterFunc(2*time.Minute, func() { cmd.Process.Kill() })
	err = cmd.Wait()
	timer.Stop()
	out := <-done
	r.Close()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		return "", 0, err
	}
	return string(out), code, nil
}

func lookPath(bin, name string) (string, error) {
	p := filepath.Join(bin, name)
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		return p, nil
	}
	return exec.LookPath(name)
}

// shellQuote renders argv as a copy-pasteable shell command line.
func shellQuote(argv []string) string {
	safe := regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)
	parts := make([]string, len(argv))
	for i, a := range argv {
		if safe.MatchString(a) {
			parts[i] = a
		} else {
			parts[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(parts, " ")
}

// readLiveValues loads the captured live snapshot so viewer and terminal
// expectations can name the actual value ({{telemetry.<name>}}).
func readLiveValues(work string, live map[string]string) error {
	raw, err := os.ReadFile(filepath.Join(work, "live", "telemetry.json"))
	if err != nil {
		return err
	}
	var t struct {
		Measurements map[string]struct {
			Value json.RawMessage `json:"value"`
		} `json:"measurements"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	for name, m := range t.Measurements {
		live["telemetry."+name] = strings.Trim(string(m.Value), `"`)
	}
	return nil
}

func substitute(s string, live map[string]string) string {
	for k, v := range live {
		s = strings.ReplaceAll(s, "{{"+k+"}}", regexp.QuoteMeta(v))
	}
	return s
}

// --- AAS viewer ---------------------------------------------------------

func b64(id string) string { return base64.RawURLEncoding.EncodeToString([]byte(id)) }

func viewerURL(cfg aasConfig, v *view) string {
	api := strings.TrimSuffix(cfg.basyxAPI, "/")
	q := url.Values{"aas": {api + "/shells/" + b64(v.AAS)}}
	if v.Submodel != "" {
		q.Set("path", api+"/submodels/"+b64(v.Submodel)+"/submodel-elements/"+v.Element)
	}
	return strings.TrimSuffix(cfg.basyxUI, "/") + "/aasviewer?" + q.Encode()
}

// captureViewer opens one viewer page and waits until every expected text
// is shown (in the page text or a field value), then captures it at the
// video's content size.
func captureViewer(debug string, cfg aasConfig, st step, live map[string]string) ([]byte, error) {
	if st.View == nil || cfg.basyxUI == "" {
		return nil, fmt.Errorf("viewer step without a view or without -basyx-ui")
	}
	var checks []string
	for _, e := range st.Expect {
		v, _ := json.Marshal(substituteText(e, live))
		checks = append(checks, "shown.includes("+string(v)+")")
	}
	ready := `(() => { const shown = document.body.innerText + ' ' + [...document.querySelectorAll('input,textarea')].map(e => e.value).join(' '); return ` +
		strings.Join(append(checks, "document.getAnimations().every(a => a.playState !== 'running')"), " && ") + `; })()`
	p, err := newPage(debug)
	if err != nil {
		return nil, err
	}
	defer p.close()
	// The frame's aspect ratio at a larger viewport, so the element details
	// (value, unit) are in view; the frame scales it down.
	png, err := p.captureAt(viewerURL(cfg, st.View), 1600, 820, ready, false)
	if err != nil {
		return nil, err
	}
	return png, nil
}

func substituteText(s string, live map[string]string) string {
	for k, v := range live {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

// basyxReset deletes every shell, submodel and concept description, so the
// next upload (BaSyx refuses duplicate identifiers) starts clean.
func basyxReset(api string) error {
	for _, kind := range []string{"shells", "submodels", "concept-descriptions"} {
		resp, err := http.Get(strings.TrimSuffix(api, "/") + "/" + kind)
		if err != nil {
			return err
		}
		var list struct {
			Result []struct {
				ID string `json:"id"`
			} `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&list)
		resp.Body.Close()
		if err != nil {
			return err
		}
		for _, item := range list.Result {
			req, _ := http.NewRequest(http.MethodDelete, strings.TrimSuffix(api, "/")+"/"+kind+"/"+b64(item.ID), nil)
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			r.Body.Close()
			if r.StatusCode >= 300 {
				return fmt.Errorf("DELETE %s %s: %s", kind, item.ID, r.Status)
			}
		}
	}
	return nil
}

// --- pages --------------------------------------------------------------

type pageServer struct {
	scenes map[string]*scene
	work   string
}

func (s *pageServer) start() (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/scene/", func(w http.ResponseWriter, r *http.Request) {
		sc, ok := s.scenes[strings.TrimPrefix(r.URL.Path, "/scene/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		page, err := s.render(sc, r.URL.Query().Get("mode") == "full")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	})
	mux.HandleFunc("/image/", func(w http.ResponseWriter, r *http.Request) {
		sc, ok := s.scenes[strings.TrimPrefix(r.URL.Path, "/image/")]
		if !ok || sc.image == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, sc.image)
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go http.Serve(l, mux)
	return "http://" + l.Addr().String(), nil
}

const pageStyle = `<style>
html,body{margin:0;background:#0b0f14;color:#d6dde6;font-family:"DejaVu Sans",sans-serif}
body.video{width:1280px;height:720px;overflow:hidden}
.cap{height:64px;box-sizing:border-box;padding:0 28px;display:flex;align-items:center;background:#1f6feb;color:#fff;font:600 20px/1.25 "DejaVu Sans",sans-serif}
.term{margin:18px 28px;border-radius:8px;background:#11161d;overflow:hidden}
.video .term{height:620px;display:flex;flex-direction:column}
.bar{background:#1c232c;padding:7px 12px;color:#9aa4b2;font:13px/1.2 "DejaVu Sans",sans-serif;flex:none}
.scroll{overflow:hidden;flex:1}
pre{margin:0;padding:12px 16px;font:16px/1.45 "DejaVu Sans Mono",monospace;white-space:pre-wrap;overflow-wrap:anywhere}
.ps{color:#4ade80}.cmd{color:#fff}.cur{background:#d6dde6;color:#11161d}
.esc{color:#facc15;background:#3b2f06;border-radius:3px}
.cols{display:flex;flex-wrap:wrap;gap:14px;margin:16px 28px;align-items:flex-start}
.panel.wide{flex-basis:100%}
.c2{column-count:2;column-gap:24px}
.panel{flex:1;min-width:0;border-radius:8px;background:#11161d;overflow:hidden}
.panel pre{font-size:12px;line-height:1.35}
.panel.grow{flex:1.7}
.k{color:#67e8f9}.s{color:#a5d6a7}.n{color:#facc15}.b{color:#f0abfc}
table{border-collapse:collapse;font:12px/1.35 "DejaVu Sans",sans-serif;margin:10px}
td,th{border:1px solid #2a323c;padding:3px 6px;text-align:left;vertical-align:top;white-space:nowrap}
th{color:#9aa4b2}tr.hl td{background:#16361f}
.card{height:720px;display:flex;flex-direction:column;justify-content:center;padding:0 90px;box-sizing:border-box}
.card h1{font:700 44px/1.2 "DejaVu Sans",sans-serif;margin:0 0 18px;color:#fff}
.card p{font:22px/1.5 "DejaVu Sans",sans-serif;margin:0;color:#9aa4b2;white-space:pre-line}
.shot{display:block;width:1280px;height:656px;object-fit:contain;object-position:top}
</style>`

// render produces a scene page. full: the whole content at natural height
// (baselines); otherwise a fixed 1280x720 video frame whose JavaScript
// setChars(n) types the command n characters at a time.
func (s *pageServer) render(sc *scene, full bool) (string, error) {
	var b strings.Builder
	class := "video"
	if full {
		class = "full"
	}
	b.WriteString(`<!doctype html><meta charset="utf-8"><title>` + html.EscapeString(sc.Name) + `</title>` + pageStyle + `<body class="` + class + `"><div id="scene">`)
	if sc.Kind != "card" {
		b.WriteString(`<div class="cap">` + visible(sc.Caption) + `</div>`)
	}
	switch sc.Kind {
	case "card":
		b.WriteString(`<div class="card"><h1>` + visible(sc.Title) + `</h1><p>` + visible(sc.Subtitle) + `</p></div>`)
	case "terminal":
		cmd, _ := json.Marshal(sc.command)
		b.WriteString(`<div class="term"><div class="bar">shell</div><div class="scroll"><pre><span class="ps">$ </span><span class="cmd" id="cmd">` +
			html.EscapeString(sc.command) + `</span><span class="cur" id="cur"> </span>` + "\n" + `<span id="out">` + ansiHTML(visibleRaw(sc.output)) + `</span></pre></div></div>`)
		b.WriteString(`<script>const full=` + string(cmd) + `;function setChars(n){document.getElementById('cmd').textContent=full.slice(0,n);` +
			`document.getElementById('out').style.display=n>=full.length+1?'':'none';document.getElementById('cur').style.display=n>=full.length+1?'none':'';` +
			`const s=document.querySelector('.scroll');s.scrollTop=s.scrollHeight}` +
			`document.getElementById('cur').style.display='none';document.querySelector('.scroll').scrollTop=1e9</script>`)
	case "files":
		b.WriteString(`<div class="cols">`)
		for _, p := range sc.Panels {
			raw, err := os.ReadFile(filepath.Join(s.work, p.Path))
			if err != nil {
				return "", fmt.Errorf("%s: %w", sc.Name, err)
			}
			cls := "panel"
			if p.Wide {
				cls += " wide"
			}
			if p.Columns == 2 {
				cls += " grow"
			}
			b.WriteString(`<div class="` + cls + `"><div class="bar">` + visible(p.Title) + ` — ` + visible(p.Path) + `</div>`)
			switch p.Kind {
			case "csv":
				b.WriteString(csvTable(string(raw), p.Rows, p.Fields))
			case "json":
				pre := `<pre>`
				if p.Columns == 2 {
					pre = `<pre class="c2">`
				}
				b.WriteString(pre + highlightJSON(string(raw)) + `</pre>`)
			default:
				b.WriteString(`<pre>` + visible(string(raw)) + `</pre>`)
			}
			b.WriteString(`</div>`)
		}
		b.WriteString(`</div>`)
	case "viewer":
		b.WriteString(`<img class="shot" src="/image/` + url.PathEscape(sc.Name) + `" alt="">`)
	}
	b.WriteString(`</div>`)
	if !full {
		// A scene taller than the frame is scaled down to fit, never cut.
		b.WriteString(`<script>(()=>{const e=document.getElementById('scene');const h=e.scrollHeight;if(h>720){e.style.zoom=(720/h).toFixed(4)}})()</script>`)
	}
	b.WriteString(`</body>`)
	return b.String(), nil
}

// bidi formatting characters reorder displayed text; they are shown as
// escapes, never interpreted (as in hades top and the console, Hades-044).
var bidi = map[rune]bool{0x061c: true, 0x200e: true, 0x200f: true, 0x202a: true, 0x202b: true, 0x202c: true, 0x202d: true, 0x202e: true,
	0x2066: true, 0x2067: true, 0x2068: true, 0x2069: true}

// visible HTML-escapes text and shows control and bidi characters as
// marked escapes.
func visible(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || (r >= 0x7f && r < 0xa0) || bidi[r]:
			fmt.Fprintf(&b, `<span class="esc">\u%04X</span>`, r)
		default:
			b.WriteString(html.EscapeString(string(r)))
		}
	}
	return b.String()
}

// visibleRaw makes command output safe for ansiHTML (which escapes HTML
// itself): control and bidi characters other than SGR sequences become
// visible escapes. hades output carries no SGR, so ESC is always shown.
func visibleRaw(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20 || (r >= 0x7f && r < 0xa0) || bidi[r]:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var jsonToken = regexp.MustCompile(`("(?:[^"\\]|\\.)*")(\s*:)?|(-?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)|\b(true|false|null)\b`)

// highlightJSON colours keys, strings, numbers and literals.
func highlightJSON(src string) string {
	var b strings.Builder
	last := 0
	for _, m := range jsonToken.FindAllStringSubmatchIndex(src, -1) {
		b.WriteString(visible(src[last:m[0]]))
		switch {
		case m[2] >= 0 && m[4] >= 0:
			b.WriteString(`<span class="k">` + visible(src[m[2]:m[3]]) + `</span>` + visible(src[m[4]:m[5]]))
		case m[2] >= 0:
			b.WriteString(`<span class="s">` + visible(src[m[2]:m[3]]) + `</span>`)
		case m[6] >= 0:
			b.WriteString(`<span class="n">` + visible(src[m[6]:m[7]]) + `</span>`)
		default:
			b.WriteString(`<span class="b">` + visible(src[m[8]:m[9]]) + `</span>`)
		}
		last = m[1]
	}
	b.WriteString(visible(src[last:]))
	return b.String()
}

// csvTable renders a CSV file as a table with the chosen data rows marked.
func csvTable(src string, rows []int, fields []string) string {
	r := newCSV(src)
	if len(fields) > 0 && len(r) > 0 {
		var keep []int
		for _, f := range fields {
			for i, h := range r[0] {
				if h == f {
					keep = append(keep, i)
				}
			}
		}
		for n, rec := range r {
			var sel []string
			for _, i := range keep {
				if i < len(rec) {
					sel = append(sel, rec[i])
				}
			}
			r[n] = sel
		}
	}
	var b strings.Builder
	b.WriteString("<table>")
	for i, rec := range r {
		cls := ""
		for _, h := range rows {
			if h == i {
				cls = ` class="hl"`
			}
		}
		tag := "td"
		if i == 0 {
			tag = "th"
		}
		b.WriteString("<tr" + cls + ">")
		for _, cell := range rec {
			b.WriteString("<" + tag + ">" + visible(cell) + "</" + tag + ">")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	return b.String()
}

// --- video --------------------------------------------------------------

// buildVideo captures every video frame with the same pinned browser and
// assembles the MP4 and the GIF highlight reel with a pinned ffmpeg image.
func buildVideo(debug, server string, order []*scene, cfg aasConfig) error {
	dir := filepath.Join(cfg.out, "video")
	frames := filepath.Join(dir, "frames")
	if err := os.MkdirAll(frames, 0o755); err != nil {
		return err
	}
	var list, gif strings.Builder
	n := 0
	add := func(png []byte, seconds float64, into *strings.Builder) (string, error) {
		name := fmt.Sprintf("%05d.png", n)
		n++
		if err := os.WriteFile(filepath.Join(frames, name), png, 0o644); err != nil {
			return "", err
		}
		fmt.Fprintf(into, "file 'frames/%s'\nduration %.3f\n", name, seconds)
		return name, nil
	}
	var lastFrame, lastGIF string
	total := 0.0
	for _, sc := range order {
		if !sc.Video {
			continue
		}
		p, err := newPage(debug)
		if err != nil {
			return err
		}
		if err := p.viewport(videoW, videoH); err != nil {
			p.close()
			return err
		}
		png, err := p.capture(server+"/scene/"+sc.Name, videoW, videoH, "document.getElementById('scene') !== null")
		if err != nil {
			p.close()
			return fmt.Errorf("%s: %w", sc.Name, err)
		}
		hold := sc.Hold
		if sc.Kind == "terminal" {
			// Typing: about 1.6 s whatever the length, then a beat before
			// the output appears.
			length := len([]rune(sc.command))
			steps := int(math.Min(28, math.Max(8, float64(length)/3)))
			for i := 0; i <= steps; i++ {
				chars := int(math.Ceil(float64(length) * float64(i) / float64(steps)))
				if _, err := p.evaluate(fmt.Sprintf("setChars(%d)", chars)); err != nil {
					p.close()
					return err
				}
				shot, err := p.shot()
				if err != nil {
					p.close()
					return err
				}
				d := 0.06
				if i == steps {
					d = 0.45
				}
				if lastFrame, err = add(shot, d, &list); err != nil {
					p.close()
					return err
				}
				total += d
			}
			if _, err := p.evaluate(fmt.Sprintf("setChars(%d)", length+1)); err != nil {
				p.close()
				return err
			}
			if png, err = p.shot(); err != nil {
				p.close()
				return err
			}
			if hold == 0 {
				hold = math.Min(7, 3+0.12*float64(strings.Count(sc.output, "\n")))
			}
		}
		p.close()
		if hold == 0 {
			hold = map[string]float64{"card": 4, "files": 8, "viewer": 6}[sc.Kind]
		}
		if lastFrame, err = add(png, hold, &list); err != nil {
			return err
		}
		total += hold
		if sc.Highlight {
			name := lastFrame
			fmt.Fprintf(&gif, "file 'frames/%s'\nduration 2.5\n", name)
			lastGIF = name
		}
	}
	// The concat demuxer needs the last file repeated for its duration to count.
	fmt.Fprintf(&list, "file 'frames/%s'\n", lastFrame)
	fmt.Fprintf(&gif, "file 'frames/%s'\n", lastGIF)
	if err := os.WriteFile(filepath.Join(dir, "frames.txt"), []byte(list.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "highlights.txt"), []byte(gif.String()), 0o644); err != nil {
		return err
	}
	ff := func(args ...string) error {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		run := []string{"run", "--rm", "-v", abs + ":/work", "-w", "/work"}
		if uid := os.Getuid(); uid >= 0 {
			run = append(run, "--user", fmt.Sprintf("%d:%d", uid, os.Getgid()))
		}
		// Bypass the image's s6 init, which refuses a non-root --user.
		run = append(append(run, "--entrypoint", "/usr/local/bin/ffmpeg", cfg.ffmpeg), append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...)
		if out, err := exec.Command("docker", run...).CombinedOutput(); err != nil {
			return fmt.Errorf("ffmpeg %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	// Deterministic encode: one thread, bit-exact flags, no metadata.
	if err := ff("-f", "concat", "-safe", "0", "-i", "frames.txt", "-vf", "fps=25,format=yuv420p",
		"-c:v", "libx264", "-preset", "veryslow", "-tune", "stillimage", "-crf", "30", "-threads", "1",
		"-movflags", "+faststart", "-fflags", "+bitexact", "-flags:v", "+bitexact", "-map_metadata", "-1", "aas-demo.mp4"); err != nil {
		return err
	}
	if err := ff("-f", "concat", "-safe", "0", "-i", "highlights.txt", "-vf",
		"fps=2,scale=800:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=96:stats_mode=full[p];[b][p]paletteuse=dither=none",
		"-loop", "0", "-threads", "1", "aas-demo.gif"); err != nil {
		return err
	}
	fmt.Printf("%-28s %.0f s, %d frames -> %s\n", "aas-demo video", total, n, filepath.Join(dir, "aas-demo.mp4"))
	if total < 120 || total > 240 {
		return fmt.Errorf("video runs %.0f s; the ticket asks for 2 to 4 minutes", total)
	}
	if cfg.refresh && cfg.videoDir != "" {
		for _, f := range []string{"aas-demo.mp4", "aas-demo.gif"} {
			raw, err := os.ReadFile(filepath.Join(dir, f))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(cfg.videoDir, f), raw, 0o644); err != nil {
				return err
			}
		}
		fmt.Printf("%-28s committed copy refreshed in %s\n", "aas-demo video", cfg.videoDir)
	}
	return nil
}

// newCSV parses a CSV file for display; a malformed file is shown line by line.
func newCSV(src string) [][]string {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(src, "\ufeff")))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		records = nil
		for _, line := range strings.Split(strings.TrimRight(src, "\n"), "\n") {
			records = append(records, []string{line})
		}
	}
	return records
}
