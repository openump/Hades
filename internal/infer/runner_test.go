package infer

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner serves one response per connection on a Unix socket.
func fakeRunner(t *testing.T, respond func(request string) string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "r.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				if reply := respond(line); reply != "" {
					conn.Write([]byte(reply))
				}
			}()
		}
	}()
	return socket
}

func externalInput() Input {
	return Input{Windows: map[string][]float64{"temp": {1, 2, 3}}, GoodRatios: map[string]float64{"temp": 1}}
}

func TestExternalRunnerRoundTrip(t *testing.T) {
	socket := fakeRunner(t, func(request string) string {
		if !strings.Contains(request, `"model":"m@1.0.0"`) {
			return `{"error":"wrong model"}` + "\n"
		}
		return `{"score":0.4,"confidence":0.9,"contributions":{"temp":0.4},"soft_sensors":{}}` + "\n"
	})
	r := &externalRunner{socket: socket, model: "m@1.0.0"}
	result, err := r.Infer(context.Background(), externalInput())
	if err != nil || result.Score != 0.4 {
		t.Fatal(result, err)
	}
}

func TestExternalRunnerFailuresAreBounded(t *testing.T) {
	cases := map[string]struct {
		reply string
		want  string
	}{
		"runner error":         {`{"error":"model crashed"}` + "\n", "model crashed"},
		"score out of range":   {`{"score":7,"confidence":1}` + "\n", "out-of-range"},
		"malformed response":   {"not json\n", "runner response"},
		"oversized response":   {`{"score":0.1,"pad":"` + strings.Repeat("x", maxRunnerMessage) + `"}` + "\n", "runner response"},
		"closes without reply": {"", "without a response"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			socket := fakeRunner(t, func(string) string { return tc.reply })
			_, err := (&externalRunner{socket: socket, model: "m@1"}).Infer(context.Background(), externalInput())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestExternalRunnerHonorsDeadline(t *testing.T) {
	socket := fakeRunner(t, func(string) string { time.Sleep(2 * time.Second); return "" })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := (&externalRunner{socket: socket, model: "m@1"}).Infer(ctx, externalInput()); err == nil {
		t.Fatal("a stuck runner must fail")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline not honored: %v", elapsed)
	}
}
