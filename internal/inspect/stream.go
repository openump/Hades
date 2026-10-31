package inspect

import (
	"sync"
)

// MaxStreams bounds concurrent live inspector streams.
const MaxStreams = 4

// Broadcaster signals live streams that the model changed. Each client holds a
// one-slot channel: a pending signal is coalesced, never queued, so a slow or
// stalled browser can never block the publisher or grow memory. A client always
// renders the newest snapshot when it next reads.
type Broadcaster struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func NewBroadcaster() *Broadcaster { return &Broadcaster{clients: map[chan struct{}]struct{}{}} }

// Subscribe registers a stream, or reports false when MaxStreams are active.
func (b *Broadcaster) Subscribe() (chan struct{}, func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.clients) >= MaxStreams {
		return nil, nil, false
	}
	ch := make(chan struct{}, 1)
	b.clients[ch] = struct{}{}
	return ch, func() {
		b.mu.Lock()
		delete(b.clients, ch)
		b.mu.Unlock()
	}, true
}

// Notify signals every stream without blocking.
func (b *Broadcaster) Notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.clients {
		select {
		case ch <- struct{}{}:
		default: // a signal is already pending; coalesce
		}
	}
}

func (b *Broadcaster) Active() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.clients)
}
