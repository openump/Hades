package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MemoryBudgetBytes is the stack's resident memory budget on the reference IPC.
const MemoryBudgetBytes = 250 << 20

const (
	reset, bold, dim, inverse = "\x1b[0m", "\x1b[1m", "\x1b[2m", "\x1b[7m"
	red, green, yellow, cyan  = "\x1b[31m", "\x1b[32m", "\x1b[33m", "\x1b[36m"
)

type painter struct{ color bool }

func (p painter) c(code, s string) string {
	if !p.color {
		return s
	}
	return code + s + reset
}

func (p painter) quality(q string) string {
	switch q {
	case "GOOD":
		return p.c(green, q)
	case "UNCERTAIN":
		return p.c(yellow, q)
	case "BAD", "STALE":
		return p.c(red, q)
	}
	return q
}

// Frame renders the interactive view: header, pane tabs and the focused
// pane, cut to the terminal size. It is a pure function of its inputs.
func Frame(s *State, width, height int, now time.Time, color bool) []string {
	p := painter{color}
	lines := header(s, p, now)
	var tabs []string
	for i, t := range paneTitles {
		if Pane(i) == s.Focus {
			tabs = append(tabs, p.c(inverse, " "+t+" "))
		} else {
			tabs = append(tabs, " "+t+" ")
		}
	}
	lines = append(lines, strings.Join(tabs, "|"), strings.Repeat("─", max(width, 1)))
	body := pane(s, s.Focus, p, now)
	room := height - len(lines) - 1
	if room < 1 {
		room = 1
	}
	if s.Scroll[s.Focus] > max(len(body)-room, 0) {
		s.Scroll[s.Focus] = max(len(body)-room, 0)
	}
	start := s.Scroll[s.Focus]
	end := min(start+room, len(body))
	lines = append(lines, body[start:end]...)
	for len(lines) < height-1 {
		lines = append(lines, "")
	}
	help := "Tab/1-4 pane  j/k scroll  / filter  Esc clear  q quit   read-only"
	if s.Filtering {
		help = "filter: " + clean(s.Filter) + "_   (Enter keep, Esc clear)"
	}
	lines = append(lines, p.c(dim, help))
	for i := range lines {
		lines[i] = clip(lines[i], width)
	}
	return lines
}

// Once renders every pane in full, without colour unless asked, for support
// bundles, CI and non-TTY pipes.
func Once(s *State, now time.Time, color bool) string {
	p := painter{color}
	lines := header(s, p, now)
	for i := Pane(0); i < paneCount; i++ {
		lines = append(lines, "", p.c(bold, "== "+paneTitles[i]+" =="))
		lines = append(lines, pane(s, i, p, now)...)
	}
	return strings.Join(lines, "\n") + "\n"
}

func header(s *State, p painter, now time.Time) []string {
	title := p.c(bold, "hades top") + "  " + now.UTC().Format(time.RFC3339)
	var out []string
	if s.Snapshot != nil {
		title += fmt.Sprintf("  snapshot %s", s.Snapshot.GeneratedAt.UTC().Format("15:04:05"))
	}
	out = append(out, title)
	if s.Err != nil {
		since := "never connected"
		if !s.LastOK.IsZero() {
			since = "last data " + now.Sub(s.LastOK).Truncate(time.Second).String() + " ago"
		}
		out = append(out, p.c(inverse+red, " DAEMON UNREACHABLE ")+" "+clean(s.Err.Error())+" ("+since+"; retrying)")
	}
	return out
}

func pane(s *State, which Pane, p painter, now time.Time) []string {
	if s.Snapshot == nil {
		return []string{"waiting for the first snapshot from hades-inspector"}
	}
	switch which {
	case PaneServices:
		return services(s, p, now)
	case PaneTelemetry:
		return telemetry(s, p, now)
	case PaneStorage:
		return storage(s, p, now)
	default:
		return actuator(s, p)
	}
}

func services(s *State, p painter, now time.Time) []string {
	names := sortedKeys(s.Snapshot.Services)
	if len(names) == 0 {
		return []string{"no supervised services configured (hades-inspector -services)"}
	}
	rows := [][]string{{"SERVICE", "READY", "DIGEST", "PID", "UPTIME", "RSS", "CPU", "REVISION"}}
	var total int64
	for _, path := range names {
		e, _ := s.Snapshot.Services[path].(map[string]any)
		ready, digest := "no", "unverified"
		if b, _ := e["ready"].(bool); b {
			ready = p.c(green, "yes")
		} else {
			ready = p.c(red, "no")
		}
		switch v, known := e["digest_verified"].(bool); {
		case !known:
			digest = p.c(yellow, "unknown") // supervisor not reachable; not a mismatch
		case v:
			digest = p.c(green, "verified")
		default:
			digest = p.c(red, "MISMATCH")
		}
		pid, _ := e["child_pid"].(float64)
		uptime, rss, cpu := "-", "n/a", "n/a"
		if started, err := time.Parse(time.RFC3339Nano, str(e["started_at"])); err == nil && !started.IsZero() {
			uptime = now.Sub(started).Truncate(time.Second).String()
		}
		if r, ok := s.Resources[int(pid)]; ok && pid > 0 {
			rss = mib(r.RSSBytes)
			if r.CPUKnown {
				cpu = fmt.Sprintf("%.1f%%", r.CPUPercent)
			}
			total += r.RSSBytes
		}
		name := clean(strings.TrimSuffix(filepath.Base(path), ".json"))
		pidText := "-"
		if pid > 0 {
			pidText = fmt.Sprint(int(pid))
		}
		rows = append(rows, []string{name, ready, digest, pidText, uptime, rss, cpu, clean(str(e["revision"]))})
	}
	out := table(rows)
	for _, path := range names {
		e, _ := s.Snapshot.Services[path].(map[string]any)
		if msg := str(e["error"]); msg != "" {
			out = append(out, p.c(red, "! "+clean(strings.TrimSuffix(filepath.Base(path), ".json")+": "+msg)))
		}
	}
	if total > 0 {
		out = append(out, "", "stack RSS "+bar(float64(total)/MemoryBudgetBytes, 30, p)+" "+mib(total)+" of "+mib(MemoryBudgetBytes)+" budget")
	} else {
		out = append(out, "", p.c(dim, "RSS/CPU from /proc are shown when hades top runs on the node (Linux)"))
	}
	return out
}

func telemetry(s *State, p painter, now time.Time) []string {
	rows := [][]string{{"TOPIC", "SIGNAL", "VALUE", "UNIT", "QUALITY", "AGE"}}
	filter := strings.ToLower(s.Filter)
	for _, a := range s.Snapshot.Assets {
		for _, name := range sortedKeys(a.Signals) {
			sig := a.Signals[name]
			key := a.Topic + "/" + name
			if filter != "" && !strings.Contains(strings.ToLower(key), filter) {
				continue
			}
			rows = append(rows, []string{clean(a.Topic), clean(name), clean(fmt.Sprint(sig.Value)), clean(sig.Unit), p.quality(clean(sig.Quality)), now.Sub(sig.ObservedAt).Truncate(time.Second).String()})
		}
	}
	out := table(rows)
	if len(rows) == 1 {
		out = append(out, "no matching signals")
	}
	if s.Filter != "" {
		out = append([]string{p.c(cyan, "filter: "+clean(s.Filter))}, out...)
	}
	return append(out, "", p.c(dim, "update rates, southbound PLC RTT and guardrail counters are not exposed read-only (the snapshot holds the latest value only)"))
}

func storage(s *State, p painter, now time.Time) []string {
	var out []string
	q := s.Snapshot.Quarantine
	if len(q) == 0 {
		out = append(out, "quarantine: not configured (hades-inspector -normalizer-health)")
	} else if msg := str(q["error"]); msg != "" {
		out = append(out, p.c(red, "quarantine: "+clean(msg)))
	} else {
		reserve := p.c(green, "ok")
		if ok, _ := q["disk_reserve_ok"].(bool); !ok {
			reserve = p.c(red, "LOW")
		}
		out = append(out, fmt.Sprintf("quarantine records %v  bytes %v  evictions %v  rejected %v  storage failures %v  disk reserve %s",
			num(q["quarantine_records"]), num(q["quarantine_bytes"]), num(q["quarantine_evictions"]), num(q["rejected"]), num(q["storage_failures"]), reserve))
	}
	out = append(out, "")
	if len(s.Snapshot.Certificates) == 0 {
		out = append(out, "certificates: none watched (hades-inspector -certificates)")
	} else {
		rows := [][]string{{"CERTIFICATE", "SUBJECT", "EXPIRES", "DAYS"}}
		for _, c := range s.Snapshot.Certificates {
			days := fmt.Sprint(c.DaysLeft)
			switch {
			case c.DaysLeft < 0:
				days = p.c(red, "EXPIRED")
			case c.DaysLeft < 30:
				days = p.c(yellow, days)
			}
			rows = append(rows, []string{clean(filepath.Base(c.Path)), clean(c.Subject), c.NotAfter.UTC().Format("2006-01-02"), days})
		}
		out = append(out, table(rows)...)
	}
	return append(out, "", p.c(dim, "WAL and transfer queue depth, cloud sync state and rejected payloads are not exposed read-only"))
}

func actuator(s *State, p painter) []string {
	if len(s.Snapshot.Receipts) == 0 {
		return []string{"no verified command receipts (hades-inspector -actuator-keys)", "", p.c(dim, "staging mailbox, watchdog lease and autonomy level are on the actuator's private socket, deliberately not read")}
	}
	rows := [][]string{{"RECORDED", "ASSET", "PARAMETER", "STATE", "GATES 1-5", "COMMAND"}}
	for i := len(s.Snapshot.Receipts) - 1; i >= 0; i-- { // newest first
		r := s.Snapshot.Receipts[i]
		var gates []struct {
			Gate   int    `json:"gate"`
			Result string `json:"result"`
		}
		json.Unmarshal(r.Gates, &gates)
		var marks []string
		for _, g := range gates {
			switch g.Result {
			case "PASS":
				marks = append(marks, p.c(green, "✓"))
			case "FAIL":
				marks = append(marks, p.c(red, "✗"))
			default:
				marks = append(marks, p.c(dim, "·"))
			}
		}
		state := clean(r.State)
		switch {
		case state == "EXECUTED" || state == "SHADOW_LOGGED":
			state = p.c(green, state)
		case state == "AWAITING_OPERATOR_CONFIRMATION":
			state = p.c(yellow, state)
		default:
			state = p.c(red, state)
		}
		rows = append(rows, []string{r.RecordedAt.UTC().Format("15:04:05"), clean(r.Asset), clean(r.Parameter), state, strings.Join(marks, ""), clean(r.CommandID)})
	}
	return append(table(rows), "", p.c(dim, "signed receipts only; staging mailbox, watchdog lease and autonomy level are on the actuator's private socket, deliberately not read"))
}

// table aligns columns by visible width (ANSI codes do not count).
func table(rows [][]string) []string {
	widths := map[int]int{}
	for _, r := range rows {
		for i, cell := range r {
			widths[i] = max(widths[i], visible(cell))
		}
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var b strings.Builder
		for i, cell := range r {
			b.WriteString(cell)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-visible(cell)+2))
			}
		}
		out = append(out, b.String())
	}
	return out
}

func bar(frac float64, width int, p painter) string {
	frac = min(max(frac, 0), 1)
	n := int(frac*float64(width) + 0.5)
	code := green
	if frac > 0.8 {
		code = red
	} else if frac > 0.6 {
		code = yellow
	}
	return "[" + p.c(code, strings.Repeat("█", n)) + strings.Repeat("░", width-n) + "]"
}

// visible counts printed runes, skipping ANSI escape sequences.
func visible(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case esc:
			if r >= '@' && r <= '~' && r != '[' {
				esc = false
			}
		case r == 0x1b:
			esc = true
		default:
			n++
		}
	}
	return n
}

// clip cuts a line to width visible columns, keeping escape sequences and
// resetting colour if anything was cut.
func clip(s string, width int) string {
	if visible(s) <= width {
		return s
	}
	var b strings.Builder
	n, esc := 0, false
	for _, r := range s {
		switch {
		case esc:
			b.WriteRune(r)
			if r >= '@' && r <= '~' && r != '[' {
				esc = false
			}
		case r == 0x1b:
			esc = true
			b.WriteRune(r)
		case n < width:
			b.WriteRune(r)
			n++
		}
	}
	if strings.Contains(s, "\x1b") {
		b.WriteString(reset)
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) string {
	if f, ok := v.(float64); ok {
		return fmt.Sprint(int64(f))
	}
	return "-"
}

func mib(b int64) string { return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20)) }
