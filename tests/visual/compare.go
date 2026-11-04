package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

// Rect is a masked region, in pixels, excluded from comparison.
type Rect struct{ X, Y, W, H int }

func (r Rect) contains(x, y int) bool { return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H }

// Comparison is the result of comparing a capture with its baseline.
type Comparison struct {
	Pixels, Different int
	SizeMismatch      bool
}

// Ratio is the fraction of compared pixels that differ.
func (c Comparison) Ratio() float64 {
	if c.Pixels == 0 {
		return 0
	}
	return float64(c.Different) / float64(c.Pixels)
}

// Compare counts pixels whose colour differs from the baseline by more than
// tolerance in any channel, outside the masks, and returns a diff image: the
// capture dimmed, with differing pixels in red. Captures of different sizes
// are a mismatch (the layout changed).
func Compare(baseline, actual image.Image, masks []Rect, tolerance uint8) (Comparison, image.Image) {
	b, a := baseline.Bounds(), actual.Bounds()
	diff := image.NewRGBA(a)
	if b.Dx() != a.Dx() || b.Dy() != a.Dy() {
		return Comparison{SizeMismatch: true}, diff
	}
	var c Comparison
	for y := 0; y < a.Dy(); y++ {
		for x := 0; x < a.Dx(); x++ {
			pa := color.RGBAModel.Convert(actual.At(a.Min.X+x, a.Min.Y+y)).(color.RGBA)
			masked := false
			for _, m := range masks {
				if m.contains(x, y) {
					masked = true
					break
				}
			}
			dimmed := color.RGBA{pa.R / 3, pa.G / 3, pa.B / 3, 255}
			if masked {
				diff.Set(x, y, color.RGBA{0, 0, 90, 255})
				continue
			}
			c.Pixels++
			pb := color.RGBAModel.Convert(baseline.At(b.Min.X+x, b.Min.Y+y)).(color.RGBA)
			if channelDiff(pa.R, pb.R) > tolerance || channelDiff(pa.G, pb.G) > tolerance || channelDiff(pa.B, pb.B) > tolerance {
				c.Different++
				diff.Set(x, y, color.RGBA{255, 40, 40, 255})
			} else {
				diff.Set(x, y, dimmed)
			}
		}
	}
	return c, diff
}

func channelDiff(a, b uint8) uint8 {
	if a > b {
		return a - b
	}
	return b - a
}

func readPNG(path string) (image.Image, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return img, nil
}

func writePNG(path string, img image.Image) error {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
