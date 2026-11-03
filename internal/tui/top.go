// Package tui is a read-only terminal dashboard for one Hades node. It polls
// hades-inspector's loopback snapshot and never writes to a service, broker,
// actuator or PLC: its only network call is an HTTP GET.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openump/Hades/internal/inspect"
)

// MaxSnapshotBytes bounds what one poll may read.
const MaxSnapshotBytes = 4 << 20

// Pane identifies one of the four views.
type Pane int

const (
	PaneServices Pane = iota
	PaneTelemetry
	PaneStorage
	PaneActuator
	paneCount
)

var paneTitles = [paneCount]string{"1 Supervisor & resources", "2 UNS telemetry", "3 Quarantine & certificates", "4 Actuator receipts"}

// State is everything the renderer needs. Key handling and rendering are pure
// functions of State, so they are tested without a terminal.
type State struct {
	Focus     Pane
	Scroll    [paneCount]int
	Filter    string
	Filtering bool // typing after '/'
	Quit      bool

	Snapshot  *inspect.Snapshot
	Err       error            // last poll error; non-nil shows DAEMON UNREACHABLE
	LastOK    time.Time        // last successful poll
	Resources map[int]Resource // by child PID
}

// Resource is a supervised child's RSS and CPU, read from /proc where
// available.
type Resource struct {
	RSSBytes   int64
	CPUPercent float64
	CPUKnown   bool // false on the first sample: CPU needs two samples
	cpuTicks   uint64
	at         time.Time
}

// Key applies one keypress. Navigation only: no key can issue a write.
func (s *State) Key(k string) {
	if s.Filtering {
		switch k {
		case "enter":
			s.Filtering = false
		case "esc":
			s.Filtering, s.Filter = false, ""
		case "backspace":
			if r := []rune(s.Filter); len(r) > 0 {
				s.Filter = string(r[:len(r)-1])
			}
		case "ctrl+c":
			s.Quit = true
		default:
			if len([]rune(k)) == 1 && len(s.Filter) < 64 {
				s.Filter += k
			}
		}
		s.Scroll[PaneTelemetry] = 0
		return
	}
	switch k {
	case "q", "ctrl+c":
		s.Quit = true
	case "tab":
		s.Focus = (s.Focus + 1) % paneCount
	case "shift+tab":
		s.Focus = (s.Focus + paneCount - 1) % paneCount
	case "1", "2", "3", "4":
		s.Focus = Pane(k[0] - '1')
	case "j", "down":
		s.Scroll[s.Focus]++
	case "k", "up":
		if s.Scroll[s.Focus] > 0 {
			s.Scroll[s.Focus]--
		}
	case "/":
		s.Focus, s.Filtering = PaneTelemetry, true
	case "esc":
		s.Filter = ""
	}
}

// Sample is one poll: the snapshot (or error) and the resources of its
// supervised children.
type Sample struct {
	Snapshot  *inspect.Snapshot
	Err       error
	At        time.Time
	Resources map[int]Resource
}

// Apply records a sample. On error the last good snapshot stays visible
// under the DAEMON UNREACHABLE banner.
func (s *State) Apply(x Sample) {
	if x.Err != nil {
		s.Err = x.Err
		return
	}
	s.Err, s.Snapshot, s.LastOK, s.Resources = nil, x.Snapshot, x.At, x.Resources
}

// Fetch reads one snapshot. Only GET is ever issued.
func Fetch(ctx context.Context, client *http.Client, endpoint string) (*inspect.Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("inspector returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxSnapshotBytes {
		return nil, fmt.Errorf("snapshot larger than %d bytes", MaxSnapshotBytes)
	}
	var snap inspect.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	return &snap, nil
}

// NewClient returns the dashboard's HTTP client. It never follows a
// redirect, so it cannot be steered off the loopback snapshot endpoint, and
// ignores proxy settings.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// clean makes an untrusted string safe to print: control characters
// (including ESC, C1 controls and newlines) and Unicode bidirectional
// formatting characters become U+FFFD, so plant data or error text can never
// drive the operator's terminal, break a row, or reorder text to disguise
// what it says (an RLO override would show "payload" as "daolyap").
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || bidiControl(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}

// bidiControl reports the Unicode bidirectional formatting characters: marks,
// embeddings, overrides and isolates.
func bidiControl(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// SnapshotURL turns an inspector base address into its snapshot endpoint.
func SnapshotURL(base string) string {
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return strings.TrimSuffix(base, "/") + "/ui/api/snapshot"
}
