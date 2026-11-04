package main

import (
	"image"
	"image/color"
	"strings"
	"testing"
)

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestCompare(t *testing.T) {
	gray := color.RGBA{100, 100, 100, 255}
	base := solid(10, 10, gray)
	if c, _ := Compare(base, solid(10, 10, gray), nil, 16); c.Different != 0 || c.Pixels != 100 {
		t.Fatal("identical images must not differ:", c)
	}
	changed := solid(10, 10, gray)
	changed.Set(3, 4, color.RGBA{200, 100, 100, 255})
	changed.Set(5, 5, color.RGBA{110, 100, 100, 255}) // within tolerance
	c, diff := Compare(base, changed, nil, 16)
	if c.Different != 1 {
		t.Fatal("exactly one pixel is beyond tolerance:", c)
	}
	if r := diff.At(3, 4).(color.RGBA); r.R != 255 || r.G > 50 {
		t.Fatal("the diff must mark the changed pixel red:", r)
	}
	if c, _ := Compare(base, changed, []Rect{{X: 3, Y: 4, W: 1, H: 1}}, 16); c.Different != 0 || c.Pixels != 99 {
		t.Fatal("a masked pixel must be excluded:", c)
	}
	if c, _ := Compare(base, solid(10, 11, gray), nil, 16); !c.SizeMismatch {
		t.Fatal("a size change is a layout change")
	}
}

func TestTerminalHTMLEscapesAndColours(t *testing.T) {
	page := TerminalHTML("t", "\x1b[1mbold\x1b[0m \x1b[32mGOOD\x1b[0m <script>alert(1)</script> \x1b[7mtab\x1b[0m")
	for _, want := range []string{"font-weight:700\">bold<", "color:#4ade80\">GOOD<", "&lt;script&gt;", "background:#d6dde6"} {
		if !strings.Contains(page, want) {
			t.Fatalf("missing %q in %s", want, page)
		}
	}
	if strings.Contains(page, "<script>") || strings.Contains(page, "\x1b") {
		t.Fatal("markup and escape bytes must never reach the page")
	}
}

func TestFixturesAreDeterministic(t *testing.T) {
	a, b := terminalFrames(), terminalFrames()
	for name, page := range a {
		if b[name] != page {
			t.Fatalf("%s renders differently on a second run", name)
		}
	}
	if len(a) != len(terminalTargets()) {
		t.Fatal("every terminal target must have a frame")
	}
	if !strings.Contains(a["top-once"], "REJECTED_SAFETY_BOUNDS") || !strings.Contains(a["top-once"], "STALE") {
		t.Fatal("the normal fixture must show receipts and every quality state")
	}
}
