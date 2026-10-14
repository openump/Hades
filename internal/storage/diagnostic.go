package storage

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Diagnostics is a lossy bounded side store, never a replacement for owned intake.
type Diagnostics struct {
	mu      sync.Mutex
	store   *Store
	Evicted uint64
}

func OpenDiagnostics(dir string) (*Diagnostics, error) {
	s, err := Open(dir, 10000, 64*1024*1024, 128*1024*1024)
	if err != nil {
		return nil, err
	}
	return &Diagnostics{store: s}, nil
}
func (d *Diagnostics) Write(topic string, payload []byte, reason error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	message := reason.Error()
	if len(message) > 512 {
		message = message[:512]
	}
	if len(topic) > 1024 {
		topic = topic[:1024]
	}
	raw, _ := json.Marshal(map[string]any{"received_at": time.Now().UTC(), "topic": topic, "reason": message, "payload_prefix": payload[:min(len(payload), 65536)], "truncated": len(payload) > 65536})
	for len(d.store.Records) >= 10000 || d.store.Bytes+len(raw)+Overhead+128 > 64*1024*1024 {
		keys := make([]string, 0, len(d.store.Records))
		for k := range d.store.Records {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			break
		}
		if err := d.store.Delete(keys[0]); err != nil {
			return err
		}
		d.Evicted++
	}
	key := fmt.Sprintf("%020d", time.Now().UnixNano())
	return d.store.Put(key, raw)
}
func (d *Diagnostics) Close() error { d.mu.Lock(); defer d.mu.Unlock(); return d.store.Close() }

func (d *Diagnostics) Stats() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	stats := d.store.Stats()
	stats["evictions"] = d.Evicted
	return stats
}
