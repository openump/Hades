package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWALBatchRecoveryTornTailAndCorruption(t *testing.T) {
	dir := t.TempDir() + "/wal"
	w, err := OpenWAL(dir, 100, 1024*1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Commit([]Mutation{{Key: "a", Value: json.RawMessage(`{"v":1}`)}, {Key: "b", Value: json.RawMessage(`{"v":2}`)}}); err != nil {
		t.Fatal(err)
	}
	if w.Syncs != 1 {
		t.Fatal(w.Syncs)
	}
	w.Close()
	path := filepath.Join(dir, "g00000001-00000001.wal")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.Write([]byte{0, 0, 0})
	f.Close()
	w, err = OpenWAL(dir, 100, 1024*1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := w.Get("b")
	if err != nil || string(raw) != `{"v":2}` {
		t.Fatal(string(raw), err)
	}
	if err = w.Commit([]Mutation{{Key: "a", Delete: true}}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	w, err = OpenWAL(dir, 100, 1024*1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Get("a"); err == nil {
		t.Fatal("deleted record revived")
	}
	w.Close()
	raw, _ = os.ReadFile(path)
	raw[10] ^= 1
	os.WriteFile(path, raw, 0600)
	if _, err = OpenWAL(dir, 100, 1024*1024, 0); err == nil {
		t.Fatal("corruption ignored")
	}
}
func TestWALAtomicBatchRefusalAndCompaction(t *testing.T) {
	w, err := OpenWAL(t.TempDir()+"/wal", 2, 1024*1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err = w.Commit([]Mutation{{Key: "a", Value: json.RawMessage(`1`)}, {Key: "b", Value: json.RawMessage(`2`)}, {Key: "c", Value: json.RawMessage(`3`)}}); err == nil {
		t.Fatal("capacity ignored")
	}
	if len(w.Keys()) != 0 {
		t.Fatal("partial transaction")
	}
	for i := 0; i < 100; i++ {
		if err = w.Commit([]Mutation{{Key: "a", Value: json.RawMessage(`1`)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Compact(); err != nil {
		t.Fatal(err)
	}
	if len(w.Keys()) != 1 {
		t.Fatal(w.Keys())
	}
}

func BenchmarkWALGroupCommit10000(b *testing.B) {
	for iteration := 0; iteration < b.N; iteration++ {
		w, err := OpenWAL(b.TempDir()+"/wal", 10000, 64*1024*1024, 0)
		if err != nil {
			b.Fatal(err)
		}
		start := time.Now()
		for first := 0; first < 10000; first += 100 {
			batch := make([]Mutation, 0, 100)
			for i := first; i < first+100; i++ {
				batch = append(batch, Mutation{Key: fmt.Sprintf("id-%05d", i), Value: json.RawMessage(`{"payload":"reference","state":"pending"}`)})
			}
			if err = w.Commit(batch); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(w.Syncs), "syncs/10000-records")
		b.ReportMetric(float64(time.Since(start).Microseconds())/1000, "write-ms")
		w.Close()
		start = time.Now()
		w, err = OpenWAL(w.dir, 10000, 64*1024*1024, 0)
		if err != nil {
			b.Fatal(err)
		}
		if len(w.Keys()) != 10000 {
			b.Fatal("recovery loss")
		}
		b.ReportMetric(float64(time.Since(start).Microseconds())/1000, "recovery-ms")
		w.Close()
	}
}
