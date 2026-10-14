package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRecoveryGatewayChild(t *testing.T) {
	url := os.Getenv("HADES_GATEWAY_CHILD")
	if url == "" {
		return
	}
	id := "gateway-volatile-id"
	fmt.Println(id)
	client := &http.Client{Timeout: time.Second}
	Submit(context.Background(), client, url, []byte(`{"message_id":"gateway-volatile-id"}`))
}
func TestRecoveryGatewayPendingSIGKILL(t *testing.T) {
	entered := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		select {
		case entered <- p["message_id"].(string):
		default:
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecoveryGatewayChild$")
	cmd.Env = append(os.Environ(), "HADES_GATEWAY_CHILD="+server.URL)
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() {
		t.Fatal("no independent source ID")
	}
	id := scanner.Text()
	select {
	case received := <-entered:
		if received != id {
			t.Fatal(received, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	// This profile has no durable acquisition store; only the external ledger can
	// count the killed pending observation. New polling cannot recover its identity.
	report, _ := json.Marshal(map[string]any{"scenario": "gateway-pending-sigkill", "produced": []string{id}, "admitted": []string{}, "lost": []string{id}, "runtime_loss_count": "unknown"})
	t.Log("RECOVERY " + string(report))
}
