package history

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRecoveryHistoryChild(t *testing.T) {
	dir := os.Getenv("HADES_HISTORY_CHILD")
	if dir == "" {
		return
	}
	cfg, raw := fixture(t)
	a, err := Open(cfg, dir)
	if err != nil {
		panic(err)
	}
	if _, err = a.Intake(cfg.Prefix+"telemetry", raw); err != nil {
		panic(err)
	}
	if os.Getenv("HADES_HISTORY_WINDOW") == "sink" {
		a.Drain(context.Background(), func(context.Context, []byte) error {
			var p map[string]any
			json.Unmarshal(raw, &p)
			fmt.Println(p["message_id"])
			time.Sleep(time.Minute)
			return nil
		})
	} else {
		var p map[string]any
		json.Unmarshal(raw, &p)
		fmt.Println(p["message_id"])
		time.Sleep(time.Minute)
	}
}
func TestRecoveryHistorySyncedAndUnknownRetirement(t *testing.T) {
	for _, window := range []string{"intake", "sink"} {
		t.Run(window, func(t *testing.T) {
			dir := t.TempDir() + "/intake"
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryHistoryChild$")
			cmd.Env = append(os.Environ(), "HADES_HISTORY_CHILD="+dir, "HADES_HISTORY_WINDOW="+window)
			out, _ := cmd.StdoutPipe()
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(out)
				if scanner.Scan() {
					ready <- scanner.Text()
				}
			}()
			var id string
			select {
			case id = <-ready:
			case <-time.After(5 * time.Second):
				t.Fatal("sync timeout")
			}
			cmd.Process.Kill()
			cmd.Wait()
			cfg, _ := fixture(t)
			a, err := Open(cfg, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if len(a.records) != 1 || len(a.pending) != 1 {
				t.Fatal(a.Stats())
			}
			writes := 0
			if err = a.Drain(context.Background(), func(context.Context, []byte) error { writes++; return nil }); err != nil {
				t.Fatal(err)
			}
			if writes != 1 {
				t.Fatal(writes)
			}
			for _, r := range a.records {
				if r.State != "api_accepted" {
					t.Fatal(r.State)
				}
			}
			report, _ := json.Marshal(map[string]any{"scenario": "history-sigkill-" + window, "produced": []string{id}, "recovered": []string{id}, "lost": []string{}, "replayed_after_unknown_outcome": window == "sink"})
			t.Log("RECOVERY " + string(report))
		})
	}
}
