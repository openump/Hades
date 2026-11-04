package main

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// terminalColors maps the SGR colours hades top uses to a fixed palette.
var terminalColors = map[string]string{"31": "#ff6b6b", "32": "#4ade80", "33": "#facc15", "36": "#67e8f9"}

// TerminalHTML renders hades top's ANSI output as a self-contained page in a
// fixed terminal style: the one renderer for -once and interactive frames.
// Only the SGR codes hades top emits are interpreted; every other byte is
// HTML-escaped text, so the page shows exactly what a terminal would print.
func TerminalHTML(title, ansi string) string {
	var b strings.Builder
	var fg string
	var bold, dim, inverse bool
	flush := func(text string) {
		if text == "" {
			return
		}
		var css []string
		if fg != "" {
			css = append(css, "color:"+fg)
		}
		if bold {
			css = append(css, "font-weight:700")
		}
		if dim {
			css = append(css, "opacity:.55")
		}
		if inverse {
			css = append(css, "background:#d6dde6;color:#11161d")
		}
		if len(css) > 0 {
			fmt.Fprintf(&b, `<span style="%s">%s</span>`, strings.Join(css, ";"), html.EscapeString(text))
		} else {
			b.WriteString(html.EscapeString(text))
		}
	}
	pos := 0
	for _, m := range sgr.FindAllStringSubmatchIndex(ansi, -1) {
		flush(ansi[pos:m[0]])
		pos = m[1]
		codes := ansi[m[2]:m[3]]
		if codes == "" {
			codes = "0"
		}
		for _, c := range strings.Split(codes, ";") {
			switch c {
			case "0":
				fg, bold, dim, inverse = "", false, false, false
			case "1":
				bold = true
			case "2":
				dim = true
			case "7":
				inverse = true
			default:
				if color, ok := terminalColors[c]; ok {
					fg = color
				}
			}
		}
	}
	flush(ansi[pos:])
	return `<!doctype html><meta charset="utf-8"><title>` + html.EscapeString(title) + `</title><style>
html,body{margin:0;background:#0b0f14}
.term{display:inline-block;margin:16px;border-radius:8px;background:#11161d;overflow:hidden}
.bar{background:#1c232c;padding:7px 12px;color:#9aa4b2;font:12px/1.2 "DejaVu Sans",sans-serif}
pre{margin:0;padding:12px 16px;color:#d6dde6;font:13px/1.4 "DejaVu Sans Mono",monospace;white-space:pre}
</style><div class="term" id="terminal"><div class="bar">` + html.EscapeString(title) + `</div><pre>` + b.String() + `</pre></div>`
}
