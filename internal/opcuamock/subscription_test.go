//go:build opcua_mock

package opcuamock

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openump/Hades/internal/opcuapilot"
)

func TestMonitoredItemSubscriptionOverSecuredSession(t *testing.T) {
	dir := t.TempDir()
	pki := filepath.Join(dir, "pki")
	if err := opcuapilot.InitPKI(pki); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "opc.tcp://" + listener.Addr().String()
	listener.Close()
	fault := filepath.Join(dir, "fault")
	os.WriteFile(fault, []byte("good"), 0600)
	srv, err := New(endpoint, pki, fault, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	go srv.ListenAndServe()
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var reader *opcuapilot.Reader
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if reader, err = opcuapilot.Dial(ctx, endpoint, pki, "client"); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	subscription, err := reader.Subscribe(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		values, err := subscription.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(values) != len(opcuapilot.Signals) || values[0].Value != float64(67.2) || values[0].SourceTimestamp.IsZero() {
			t.Fatal(values)
		}
	}
	if err := subscription.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
