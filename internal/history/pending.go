package history

import (
	"container/heap"
	"time"
)

// pendingHeap orders durable ownership without re-sorting the backlog per batch.
// Indexes allow retirement of exact IDs even if Resume inserts an older item
// while a request is in flight. All access is protected by Adapter.mu.
type pendingItem struct {
	id       string
	accepted time.Time
	index    int
}
type pendingHeap []*pendingItem

func (h pendingHeap) Len() int { return len(h) }
func (h pendingHeap) Less(i, j int) bool {
	if h[i].accepted.Equal(h[j].accepted) {
		return h[i].id < h[j].id
	}
	return h[i].accepted.Before(h[j].accepted)
}
func (h pendingHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *pendingHeap) Push(value any) {
	item := value.(*pendingItem)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *pendingHeap) Pop() any {
	old := *h
	item := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	item.index = -1
	return item
}
func (a *Adapter) enqueuePending(id string, r Record) {
	item := &pendingItem{id: id, accepted: r.Accepted}
	heap.Push(&a.order, item)
	a.orderIndex[id] = item
	a.pending[id] = true
}
func (a *Adapter) retirePending(id string) {
	heap.Remove(&a.order, a.orderIndex[id].index)
	delete(a.orderIndex, id)
	delete(a.pending, id)
}

// NextDrainDelay lets a successful worker immediately drain the next batch.
// Idle/backoff polls remain bounded to 100 ms for new MQTT intake and shutdown.
func (a *Adapter) NextDrainDelay() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) == 0 {
		return 100 * time.Millisecond
	}
	return min(100*time.Millisecond, max(0, time.Until(a.retryAt)))
}
