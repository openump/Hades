package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// The committed storyboard is well formed: known kinds, unique names, inputs
// that exist, patterns that compile, and a video that tells the whole story.
func TestStoryboard(t *testing.T) {
	raw, err := os.ReadFile("aas/storyboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var sb storyboard
	if err := json.Unmarshal(raw, &sb); err != nil {
		t.Fatal(err)
	}
	for _, in := range sb.Inputs {
		if _, err := os.Stat("../../" + in); err != nil {
			t.Errorf("input %s: %v", in, err)
		}
	}
	seen := map[string]bool{}
	kinds := map[string]int{}
	video, highlights := 0, 0
	for _, st := range sb.Steps {
		if seen[st.Name] {
			t.Errorf("duplicate step %s", st.Name)
		}
		seen[st.Name] = true
		kinds[st.Kind]++
		switch st.Kind {
		case "terminal":
			if len(st.Run) == 0 || st.Caption == "" {
				t.Errorf("%s: terminal step needs run and caption", st.Name)
			}
			for _, e := range st.Expect {
				if _, err := regexp.Compile(e); err != nil {
					t.Errorf("%s: %v", st.Name, err)
				}
			}
		case "viewer":
			if st.View == nil || len(st.Expect) == 0 {
				t.Errorf("%s: viewer step needs a view and expected content", st.Name)
			}
		case "files":
			if len(st.Panels) == 0 {
				t.Errorf("%s: files step without panels", st.Name)
			}
		case "card", "setup", "reset":
		default:
			t.Errorf("%s: unknown kind %q", st.Name, st.Kind)
		}
		if st.Video {
			video++
		}
		if st.Highlight {
			highlights++
		}
	}
	if video < 20 || highlights < 4 || kinds["viewer"] < 5 {
		t.Errorf("video %d scenes, %d highlights, %d viewer steps: the demo story is incomplete", video, highlights, kinds["viewer"])
	}
}

func TestVisibleEscapes(t *testing.T) {
	got := visible("a<b>\x1b[31m\u202eevil\u202c&")
	for _, want := range []string{"a&lt;b&gt;", `<span class="esc">\u001B</span>[31m`, `<span class="esc">\u202E</span>evil`, `<span class="esc">\u202C</span>`, "&amp;"} {
		if !strings.Contains(got, want) {
			t.Errorf("visible: %q lacks %q", got, want)
		}
	}
	if strings.ContainsRune(got, 0x202e) || strings.ContainsRune(got, 0x1b) {
		t.Fatal("a control or bidi character reached the page")
	}
	if raw := visibleRaw("x\x1b\u2066y\n"); raw != `x\u001B\u2066y`+"\n" {
		t.Fatalf("visibleRaw: %q", raw)
	}
}

func TestHighlightJSON(t *testing.T) {
	got := highlightJSON("{\"key<\": \"v\u202e\", \"n\": -1.5e3, \"b\": true, \"z\": null}")
	for _, want := range []string{`<span class="k">&#34;key&lt;&#34;</span>:`, `<span class="n">-1.5e3</span>`, `<span class="b">true</span>`, `<span class="b">null</span>`} {
		if !strings.Contains(got, want) {
			t.Errorf("highlight: %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "<\"") || strings.ContainsRune(got, 0x202e) {
		t.Fatalf("unescaped content: %q", got)
	}
}

// The command shown in a capture is exactly the one that ran: a shell parses
// it back into the same arguments.
func TestShellQuoteRoundTrip(t *testing.T) {
	argv := []string{"curl", "-w", "%{http_code}\n", "-F", "file=@a b.json;type=application/json", "it's", "plain-arg_1.json"}
	got, err := exec.Command("sh", "-c", `eval "set -- $0"; for a in "$@"; do printf '%s\0' "$a"; done`, shellQuote(argv)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if parts := strings.Split(strings.TrimSuffix(string(got), "\x00"), "\x00"); strings.Join(parts, "|") != strings.Join(argv, "|") {
		t.Fatalf("round trip: %q -> %q", argv, parts)
	}
}

func TestCSVTableFields(t *testing.T) {
	got := csvTable("a,b,c\n1,2,3\n4,5,6\n", []int{2}, []string{"c", "a"})
	if !strings.Contains(got, "<th>c</th><th>a</th>") || strings.Contains(got, ">b<") || !strings.Contains(got, `<tr class="hl"><td>6</td><td>4</td>`) {
		t.Fatalf("csvTable: %s", got)
	}
}
