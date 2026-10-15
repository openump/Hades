package transfer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/openump/Hades/internal/storage"
)

func TestRecoveryTransferChild(t *testing.T) {
	dir := os.Getenv("HADES_TRANSFER_CHILD")
	if dir == "" {
		return
	}
	cfg, r := fixture(t)
	q, err := Open(cfg, dir, 2, 1024*1024)
	if err != nil {
		panic(err)
	}
	if err = q.Intake(r.Topic, r.Payload); err != nil {
		panic(err)
	}
	q.Drain(context.Background(), func(context.Context, Record) error { fmt.Println(r.ID); time.Sleep(time.Minute); return nil })
}
func TestRecoveryTransferSIGKILLDuringReplay(t *testing.T) {
	dir := t.TempDir() + "/queue"
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryTransferChild$")
	cmd.Env = append(os.Environ(), "HADES_TRANSFER_CHILD="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	ready := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(out)
		if s.Scan() {
			ready <- s.Text()
		}
	}()
	var id string
	select {
	case id = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("sync timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	cfg, _ := fixture(t)
	q, err := Open(cfg, dir, 2, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if q.Stats()["pending"] != 1 {
		t.Fatal(q.Stats())
	}
	if err = q.Drain(context.Background(), func(_ context.Context, r Record) error {
		if r.ID != id {
			t.Fatal("identity changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("OUTAGE produced=1 recovered=1 lost=0 identity=%s", id)
}
func TestRecoveryTransferDiskReserveAndByteBounds(t *testing.T) {
	cfg, r := fixture(t)
	dir := t.TempDir() + "/queue"
	q, err := Open(cfg, dir, 2, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Intake(r.Topic, r.Payload); err != nil {
		t.Fatal(err)
	}
	q.store.Close()
	// Above available disk; never fill the host disk or lower production reserve.
	q.store, err = storage.Open(dir, 2, 1024*1024, 1<<60)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	var p map[string]any
	json.Unmarshal(r.Payload, &p)
	p["message_id"] = "019a1234-5678-7000-8000-000000000099"
	raw, _ := json.Marshal(p)
	if err = q.Intake(r.Topic, raw); !errors.Is(err, storage.ErrFull) {
		t.Fatal(err)
	}
	if q.Stats()["pending"] != 1 || q.Stats()["drops"] != uint64(1) {
		t.Fatal(q.Stats())
	}
	t.Log("OUTAGE disk-reserve produced=2 owned=1 refused=1 local-publication-independent")
	tiny, err := Open(cfg, t.TempDir()+"/tiny", 2, storage.Overhead)
	if err != nil {
		t.Fatal(err)
	}
	defer tiny.Close()
	if err = tiny.Intake(r.Topic, r.Payload); !errors.Is(err, storage.ErrFull) {
		t.Fatal("byte bound ignored", err)
	}
}
func TestReplayWindowAndAuthorizationPreserveOwnership(t *testing.T) {
	cfg, r := fixture(t)
	q, err := Open(cfg, t.TempDir()+"/queue", 2, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	q.Intake(r.Topic, r.Payload)
	err = q.Drain(context.Background(), func(context.Context, Record) error { return ErrAuthorization })
	if !errors.Is(err, ErrAuthorization) || q.Stats()["pending"] != 1 || q.Stats()["authorization_paused"] != true {
		t.Fatal(err, q.Stats())
	}
	q.paused = false
	owned := q.records[r.ID]
	owned.Accepted = time.Now().Add(-73 * time.Hour)
	if err = q.change(owned); err != nil {
		t.Fatal(err)
	}
	if err = q.Drain(context.Background(), func(context.Context, Record) error { t.Fatal("expired replay sent"); return nil }); err != nil {
		t.Fatal(err)
	}
	if q.Stats()["suspended"] != 1 || q.records[r.ID].Reason != "replay_window_requires_reconciliation" {
		t.Fatal(q.Stats())
	}
}

func TestReplayWindowAlsoBoundsAnAlreadyRetryingRequest(t *testing.T) {
	cfg, r := fixture(t)
	q, err := Open(cfg, t.TempDir()+"/queue", 2, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err = q.Intake(r.Topic, r.Payload); err != nil {
		t.Fatal(err)
	}
	owned := q.records[r.ID]
	clock := time.Now()
	q.now = func() time.Time { return clock }
	owned.Accepted = clock.Add(-72*time.Hour + time.Second)
	if err = q.change(owned); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err = q.Drain(context.Background(), func(context.Context, Record) error {
		calls++
		if calls == 1 {
			clock = clock.Add(2 * time.Second)
			return ErrReceipt
		}
		return nil
	})
	if err != nil || calls != 1 || q.Stats()["suspended"] != 1 {
		t.Fatal("retry exceeded reconciliation window", err, calls, q.Stats())
	}
}
