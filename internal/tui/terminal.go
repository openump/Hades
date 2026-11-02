package tui

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Terminal is a raw-mode terminal session. Restore is idempotent and always
// leaves the cursor visible, the main screen active and cooked mode restored.
type Terminal struct {
	in, out *os.File
	restore func() error
	once    sync.Once
}

// OpenTerminal switches in to raw mode and out to the alternate screen.
func OpenTerminal(in, out *os.File) (*Terminal, error) {
	restore, err := makeRaw(int(in.Fd()))
	if err != nil {
		return nil, err
	}
	t := &Terminal{in: in, out: out, restore: restore}
	io.WriteString(out, "\x1b[?1049h\x1b[?25l\x1b[2J")
	return t, nil
}

// Restore returns the terminal to the state it was opened in.
func (t *Terminal) Restore() {
	t.once.Do(func() {
		io.WriteString(t.out, "\x1b[0m\x1b[?25h\x1b[?1049l")
		t.restore()
	})
}

// Size is the terminal's columns and rows, with a fallback.
func (t *Terminal) Size() (int, int) {
	w, h, err := size(int(t.out.Fd()))
	if err != nil || w < 20 || h < 5 {
		return 100, 30
	}
	return w, h
}

// Draw repaints a full frame in one write: cursor home, each line followed by
// clear-to-end-of-line, then clear below. No full-screen clear, so no flicker.
func Draw(w io.Writer, lines []string) error {
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range lines {
		b.WriteString(l)
		b.WriteString("\x1b[K")
		if i < len(lines)-1 {
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\x1b[J")
	_, err := io.WriteString(w, b.String())
	return err
}

// escWait is how long a lone Esc waits for the rest of a sequence that SSH
// may have split across packets.
const escWait = 50 * time.Millisecond

// ReadKeys decodes keypresses from r until ctx ends or r fails. Bytes are
// read in a separate goroutine so a split escape sequence can be awaited
// with a timeout. Keys are dropped, never blocking, if the channel is full.
func ReadKeys(ctx context.Context, r io.Reader, keys chan<- string) {
	chunks := make(chan []byte, 8)
	go func() {
		defer close(chunks)
		buf := make([]byte, 256)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				select {
				case chunks <- append([]byte(nil), buf[:n]...):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var pending []byte
	open := true
	for ctx.Err() == nil {
		if len(pending) == 0 {
			if !open {
				return
			}
			select {
			case c, ok := <-chunks:
				if !ok {
					return
				}
				pending = append(pending, c...)
			case <-ctx.Done():
				return
			}
			continue
		}
		// An escape sequence still incomplete: wait briefly for the rest.
		if open && pending[0] == 0x1b && incomplete(pending) {
			select {
			case c, ok := <-chunks:
				if ok {
					pending = append(pending, c...)
					continue
				}
				open = false
			case <-time.After(escWait):
			case <-ctx.Done():
				return
			}
		}
		k, n := decode(pending)
		pending = pending[n:]
		if k == "" {
			continue
		}
		select {
		case keys <- k:
		case <-ctx.Done():
			return
		default:
		}
	}
}

// incomplete reports an Esc that may still be followed by sequence bytes.
func incomplete(b []byte) bool {
	if len(b) == 1 {
		return true
	}
	if b[1] != '[' && b[1] != 'O' {
		return false
	}
	for _, c := range b[2:] {
		if c >= '@' && c <= '~' {
			return false
		}
	}
	return true
}

// decode returns the key at the start of b and how many bytes it used.
func decode(b []byte) (string, int) {
	switch b[0] {
	case 3:
		return "ctrl+c", 1
	case 9:
		return "tab", 1
	case 13, 10:
		return "enter", 1
	case 127, 8:
		return "backspace", 1
	case 0x1b:
		if len(b) == 1 || (b[1] != '[' && b[1] != 'O') {
			return "esc", 1 // a lone Esc; the next byte is its own key
		}
		i := 2
		for i < len(b) && (b[i] < '@' || b[i] > '~') {
			i++
		}
		if i == len(b) {
			return "", len(b) // truncated sequence: discard
		}
		switch b[i] {
		case 'A':
			return "up", i + 1
		case 'B':
			return "down", i + 1
		case 'Z':
			return "shift+tab", i + 1
		}
		return "", i + 1
	}
	if b[0] >= 32 && b[0] < 127 {
		return string(rune(b[0])), 1
	}
	return "", 1
}
