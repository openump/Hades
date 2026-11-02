package tui

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// Options configure one dashboard session.
type Options struct {
	Endpoint string        // snapshot URL
	Interval time.Duration // poll interval
	Client   *http.Client  // defaults to NewClient
	ProcRoot string        // "/proc"; resources are omitted where unreadable
	Now      func() time.Time
	Resize   <-chan struct{} // repaint at once (SIGWINCH)
}

func (o *Options) defaults() {
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.Client == nil {
		o.Client = NewClient(3 * time.Second)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.ProcRoot == "" {
		o.ProcRoot = "/proc"
	}
}

// Take polls the snapshot once and samples its children's resources against
// the previous sample. It is the single poll path for -once and Run.
func Take(ctx context.Context, o Options, previous map[int]Resource) Sample {
	o.defaults()
	c, cancel := context.WithTimeout(ctx, o.Client.Timeout)
	defer cancel()
	snap, err := Fetch(c, o.Client, o.Endpoint)
	x := Sample{Snapshot: snap, Err: err, At: o.Now()}
	if err == nil {
		x.Resources = ReadResources(o.ProcRoot, PIDs(snap), previous, x.At)
	}
	return x
}

// Once takes two samples a short gap apart, so CPU can be reported, and
// returns the state for a single frame.
func TakeOnce(ctx context.Context, o Options, gap time.Duration) *State {
	o.defaults()
	s := &State{}
	first := Take(ctx, o, nil)
	if first.Err == nil && len(first.Resources) > 0 && gap > 0 {
		select {
		case <-time.After(gap):
			first = Take(ctx, o, first.Resources)
		case <-ctx.Done():
		}
	}
	s.Apply(first)
	return s
}

// Run drives the interactive dashboard until a quit key or ctx ends (for
// example on SIGTERM). Keys arrive on keys; frames are written to out. Run
// returns only after its poller goroutine has exited.
func Run(ctx context.Context, o Options, keys <-chan string, out io.Writer, size func() (int, int)) error {
	o.defaults()
	ctx, cancel := context.WithCancel(ctx)
	samples := make(chan Sample, 1)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	wg.Add(1)
	go func() { // poller: never touches State directly
		defer wg.Done()
		tick := time.NewTicker(o.Interval)
		defer tick.Stop()
		var previous map[int]Resource
		for {
			x := Take(ctx, o, previous)
			if x.Err == nil {
				previous = x.Resources
			}
			select {
			case samples <- x:
			case <-ctx.Done():
				return
			}
			select {
			case <-tick.C:
			case <-ctx.Done():
				return
			}
		}
	}()

	s := &State{}
	paint := func() error {
		w, h := size()
		return Draw(out, Frame(s, w, h, o.Now(), true))
	}
	// Ages and the unreachable timer move every second, whatever the poll interval.
	clock := time.NewTicker(min(o.Interval, time.Second))
	defer clock.Stop()
	if err := paint(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case x := <-samples:
			s.Apply(x)
		case k, ok := <-keys:
			if !ok {
				return nil
			}
			s.Key(k)
			if s.Quit {
				return nil
			}
		case <-o.Resize:
		case <-clock.C:
		}
		if err := paint(); err != nil {
			return err
		}
	}
}
