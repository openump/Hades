package storage

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRecoveryStoreChild(t *testing.T) {
	dir := os.Getenv("HADES_STORE_CHILD")
	if dir == "" {
		return
	}
	s, err := Open(dir, 2, 20000, 0)
	if err != nil {
		panic(err)
	}
	if err = s.Put("synced-id", json.RawMessage(`{"original":true}`)); err != nil {
		panic(err)
	}
	// Uncommitted temporary bytes must not replace a synced intake.
	if err = os.WriteFile(dir+"/.pending", []byte("incomplete"), 0600); err != nil {
		panic(err)
	}
	fmt.Println("synced-id")
	time.Sleep(time.Minute)
}
func TestRecoveryStoreSIGKILLAndReserveRefusal(t *testing.T) {
	dir := t.TempDir() + "/intake"
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryStoreChild$")
	cmd.Env = append(os.Environ(), "HADES_STORE_CHILD="+dir)
	output, err := cmd.StdoutPipe()
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
		scanner := bufio.NewScanner(output)
		if scanner.Scan() {
			ready <- scanner.Text()
		}
	}()
	select {
	case id := <-ready:
		if id != "synced-id" {
			t.Fatal(id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child sync timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	s, err := Open(dir, 2, 20000, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if string(s.Records["synced-id"]) != `{"original":true}` {
		t.Fatal(s.Records)
	}
	s.reserve = ^uint64(0) - 100000
	if err = s.Put("refused-id", json.RawMessage(`{}`)); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	if len(s.Records) != 1 {
		t.Fatal("reserve refusal deleted owned intake")
	}
	stats := s.Stats()
	if stats["disk_reserve_ok"] != false {
		t.Fatal(stats)
	}
	t.Log(`RECOVERY {"scenario":"synced-intake-sigkill","produced":["synced-id"],"recovered":["synced-id"],"lost":[],"refused":["refused-id"]}`)
}
func TestRecoveryCapacityAndDiagnosticEviction(t *testing.T) {
	d, err := OpenDiagnostics(t.TempDir() + "/q")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.store.maxRecords = 2
	d.store.maxBytes = 20000
	d.store.reserve = 0
	for i := 0; i < 4; i++ {
		if err = d.Write("topic", []byte("payload"), fmt.Errorf("diagnostic")); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.store.Records) != 2 || d.Evicted != 2 {
		t.Fatal(d.Stats())
	}
	t.Log(`RECOVERY {"scenario":"bounded-quarantine","produced_records":4,"retained_records":2,"evicted_records":2}`)
}
func TestRecoveryCapacityRefusesNewest(t *testing.T) {
	s, err := Open(t.TempDir()+"/intake", 2, 20000, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"owned-1", "owned-2"} {
		if err = s.Put(id, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Put("refused-3", json.RawMessage(`{}`)); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
	if len(s.Records) != 2 {
		t.Fatal(s.Records)
	}
	t.Log(`RECOVERY {"scenario":"intake-record-capacity","owned":["owned-1","owned-2"],"refused":["refused-3"],"lost":[]}`)
}
