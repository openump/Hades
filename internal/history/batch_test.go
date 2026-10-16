package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
)

func TestBatchBoundsFIFOAndAmbiguousReplay(t *testing.T) {
	cfg, _ := fixture(t)
	a, err := Open(cfg, t.TempDir()+"/intake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.ConfigureDrain(3, 65536); err != nil {
		t.Fatal(err)
	}
	var expected []byte
	for i := 0; i < 10; i++ {
		raw, e := cfg.Observation("normal", i, sample.UUID(), time.Now().UTC().Add(-time.Minute).Add(time.Duration(i)*time.Millisecond))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = a.Intake(cfg.Prefix+"telemetry", raw); e != nil {
			t.Fatal(e)
		}
		p, e := a.validate(cfg.Prefix+"telemetry", raw)
		if e != nil {
			t.Fatal(e)
		}
		lines, _, _, e := Map(cfg, p)
		if e != nil {
			t.Fatal(e)
		}
		expected = append(expected, lines...)
	}
	calls := 0
	var accepted, ambiguous []byte
	send := func(_ context.Context, body []byte) error {
		calls++
		if len(body) > 65536 {
			t.Fatal("unbounded body", len(body))
		}
		if calls == 2 {
			ambiguous = append([]byte(nil), body...)
			return context.DeadlineExceeded
		}
		if calls == 3 && !bytes.Equal(body, ambiguous) {
			t.Fatal("retry changed batch")
		}
		accepted = append(accepted, body...)
		return nil
	}
	for len(a.pending) > 0 {
		if err = a.Drain(context.Background(), send); err != nil {
			t.Fatal(err)
		}
		if calls == 2 {
			if len(a.pending) != 7 {
				t.Fatal("failed batch lost ownership", len(a.pending))
			}
			a.retryAt = time.Time{}
		}
	}
	if calls != 5 || !bytes.Equal(accepted, expected) || a.accepted != 10 {
		t.Fatal("missing, reordered or duplicated accepted observations", calls, a.accepted)
	}
	// Reopen proves per-record terminal state survives a successful batch.
	dir := a.store.Dir
	a.Close()
	a, err = Open(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.pending) != 0 || len(a.records) != 10 {
		t.Fatal(a.Stats())
	}
}

// Compare the old one-record request shape with 64-record requests over the
// same synced 10,000-record backlog. Setup/intake is excluded from drain timing.
func BenchmarkBacklog10000(b *testing.B) {
	for _, batch := range []int{1, 64} {
		b.Run(fmt.Sprintf("batch%d", batch), func(b *testing.B) {
			cfg, err := sample.Load("../..", "../../examples/site/metadata.json")
			if err != nil {
				b.Fatal(err)
			}
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				a, err := Open(cfg, b.TempDir()+"/intake")
				if err != nil {
					b.Fatal(err)
				}
				if err = a.ConfigureDrain(batch, 1048576); err != nil {
					b.Fatal(err)
				}
				stamp := time.Now().UTC().Add(-time.Minute)
				for i := 0; i < 10000; i++ {
					raw, err := cfg.Observation("normal", i, sample.UUID(), stamp.Add(time.Duration(i)*time.Millisecond))
					if err != nil {
						b.Fatal(err)
					}
					if _, err = a.Intake(cfg.Prefix+"telemetry", raw); err != nil {
						b.Fatal(err)
					}
				}
				calls := 0
				b.StartTimer()
				for len(a.pending) > 0 {
					if err = a.Drain(context.Background(), func(context.Context, []byte) error { calls++; time.Sleep(time.Millisecond); return nil }); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				if a.accepted != 10000 {
					b.Fatal(a.accepted)
				}
				a.Close()
				b.ReportMetric(float64(calls), "requests/backlog")
				b.StartTimer()
			}
		})
	}
}

func TestAcceptedBatchSurvivesPartialLocalRetirement(t *testing.T) {
	cfg, _ := fixture(t)
	a, err := Open(cfg, t.TempDir()+"/intake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var ids []string
	for i := 0; i < 3; i++ {
		raw, e := cfg.Observation("normal", i, sample.UUID(), time.Now().UTC().Add(-time.Minute).Add(time.Duration(i)*time.Millisecond))
		if e != nil {
			t.Fatal(e)
		}
		p, e := a.validate(cfg.Prefix+"telemetry", raw)
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, identity(p))
		if _, e = a.Intake(cfg.Prefix+"telemetry", raw); e != nil {
			t.Fatal(e)
		}
	}
	// Make only the second atomic retirement rename fail, after remote acceptance.
	blocked := filepath.Join(a.store.Dir, fmt.Sprintf("%x.json", sha256.Sum256([]byte(ids[1]))))
	err = a.Drain(context.Background(), func(context.Context, []byte) error {
		if e := os.Rename(blocked, blocked+".saved"); e != nil {
			t.Fatal(e)
		}
		if e := os.Mkdir(blocked, 0700); e != nil {
			t.Fatal(e)
		}
		return nil
	})
	if err == nil || a.records[ids[0]].State != "api_accepted" || len(a.pending) != 2 {
		t.Fatal("partial retirement lost ownership", err, a.Stats())
	}
	if e := os.Remove(blocked); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(blocked+".saved", blocked); e != nil {
		t.Fatal(e)
	}
	dir := a.store.Dir
	a.Close()
	a, err = Open(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if len(a.pending) != 2 {
		t.Fatal("restart lost unretired observations")
	}
	if err = a.Drain(context.Background(), func(_ context.Context, body []byte) error {
		if bytes.Contains(body, a.records[ids[0]].Lines) {
			t.Fatal("retired record replayed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(a.pending) != 0 {
		t.Fatal(a.Stats())
	}
}

func TestBatchByteLimit(t *testing.T) {
	cfg, _ := fixture(t)
	a, err := Open(cfg, t.TempDir()+"/intake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.ConfigureDrain(0, 65536) == nil || a.ConfigureDrain(256, 1048577) == nil {
		t.Fatal("invalid limits accepted")
	}
	if err = a.ConfigureDrain(256, 65536); err != nil {
		t.Fatal(err)
	}
	total := 0
	for i := 0; i < 256; i++ {
		raw, e := cfg.Observation("normal", i, sample.UUID(), time.Now().UTC().Add(-time.Minute).Add(time.Duration(i)*time.Millisecond))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = a.Intake(cfg.Prefix+"telemetry", raw); e != nil {
			t.Fatal(e)
		}
	}
	for _, r := range a.records {
		total += len(r.Lines)
	}
	if total <= 65536 {
		t.Fatal("fixture does not exercise byte bound", total)
	}
	calls, received := 0, 0
	for len(a.pending) > 0 {
		if err = a.Drain(context.Background(), func(_ context.Context, body []byte) error {
			calls++
			received += len(body)
			if len(body) > 65536 {
				t.Fatal("request exceeds byte bound", len(body))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if calls < 2 || received != total || a.accepted != 256 {
		t.Fatal(calls, received, total, a.accepted)
	}
}

func TestResumeOlderOwnershipDuringInFlightBatch(t *testing.T) {
	cfg, _ := fixture(t)
	a, err := Open(cfg, t.TempDir()+"/intake")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.ConfigureDrain(1, 65536); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		id := sample.UUID()
		raw, e := cfg.Observation("normal", i, id, time.Now().UTC().Add(-time.Minute).Add(time.Duration(i)*time.Millisecond))
		if e != nil {
			t.Fatal(e)
		}
		var p map[string]any
		if e = json.Unmarshal(raw, &p); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, p["message_id"].(string))
		if _, e = a.Intake(cfg.Prefix+"telemetry", raw); e != nil {
			t.Fatal(e)
		}
	}
	if err = a.Drain(context.Background(), func(context.Context, []byte) error { return &PermanentError{Status: 400} }); err != nil {
		t.Fatal(err)
	}
	if delay := a.NextDrainDelay(); delay != 0 {
		t.Fatal("ready backlog delayed", delay)
	}
	if err = a.Drain(context.Background(), func(context.Context, []byte) error { return a.Resume(ids[0]) }); err != nil {
		t.Fatal(err)
	}
	if len(a.pending) != 2 || a.order[0].id != cfg.Metadata["asset_id"].(string)+"/"+ids[0] {
		t.Fatal("resume corrupted FIFO ownership")
	}
	for len(a.pending) > 0 {
		if err = a.Drain(context.Background(), func(context.Context, []byte) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if delay := a.NextDrainDelay(); delay != 100*time.Millisecond {
		t.Fatal("idle worker spins", delay)
	}
}
