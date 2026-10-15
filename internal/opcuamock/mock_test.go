//go:build opcua_mock

package opcuamock

import (
	"context"
	"github.com/awcullen/opcua/client"
	"github.com/awcullen/opcua/ua"
	"github.com/openump/Hades/internal/opcuapilot"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSecuredReadOnlyMockAndFaults(t *testing.T) {
	dir := t.TempDir()
	pki := filepath.Join(dir, "pki")
	if err := opcuapilot.InitPKI(pki); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	endpoint := "opc.tcp://" + addr
	fault := filepath.Join(dir, "fault")
	os.WriteFile(fault, []byte("good"), 0600)
	srv, err := New(endpoint, pki, fault, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe() }()
	defer func() {
		srv.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var reader *opcuapilot.Reader
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		reader, err = opcuapilot.Dial(ctx, endpoint, pki, "client")
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.Namespace < 2 {
		t.Fatal("namespace padding not resolved", reader.Namespace)
	}
	values, err := reader.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if values[0].Value != float64(67.2) || values[1].Value != uint32(1) || values[2].Value != uint16(1) {
		t.Fatal(values)
	}
	// No legacy/plaintext/sign-only service endpoints are advertised.
	endpoints, err := client.GetEndpoints(ctx, &ua.GetEndpointsRequest{EndpointURL: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range endpoints.Endpoints {
		if ep.SecurityPolicyURI != opcuapilot.Policy || ep.SecurityMode != ua.MessageSecurityModeSignAndEncrypt {
			t.Fatal(ep)
		}
	}
	_, err = opcuapilot.Dial(ctx, endpoint, pki, "outsider")
	if err == nil {
		t.Fatal("untrusted application accepted")
	}
	_, err = client.Dial(ctx, endpoint, client.WithSecurityPolicyURI(ua.SecurityPolicyURINone, ua.MessageSecurityModeNone))
	if err == nil {
		t.Fatal("unencrypted anonymous session accepted")
	}
	node := ua.NodeIDString{NamespaceIndex: reader.Namespace, ID: "Temperature"}
	write, err := reader.Client.Write(ctx, &ua.WriteRequest{NodesToWrite: []ua.WriteValue{{NodeID: node, AttributeID: ua.AttributeIDValue, Value: ua.NewDataValue(float64(100), ua.Good, time.Now(), 0, time.Now(), 0)}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(write.Results) != 1 || (write.Results[0] != ua.BadUserAccessDenied && write.Results[0] != ua.BadNotWritable) {
		t.Fatal("write accepted", write)
	}
	for _, tc := range []struct {
		node   string
		status ua.StatusCode
	}{{"Unknown", ua.BadNodeIDUnknown}, {"Private", ua.BadNodeIDUnknown}} {
		response, err := reader.Client.Read(ctx, &ua.ReadRequest{NodesToRead: []ua.ReadValueID{{NodeID: ua.NodeIDString{NamespaceIndex: reader.Namespace, ID: tc.node}, AttributeID: ua.AttributeIDValue}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].StatusCode != tc.status {
			t.Fatal(tc.node, response)
		}
	}
	for _, tc := range []struct {
		mode   string
		status ua.StatusCode
	}{{"uncertain", ua.UncertainLastUsableValue}, {"bad", ua.BadSensorFailure}, {"missing", ua.BadNodeIDUnknown}} {
		os.WriteFile(fault, []byte(tc.mode), 0600)
		values, err = reader.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if values[0].StatusCode != tc.status {
			t.Fatal(values)
		}
	}
	os.WriteFile(fault, []byte("delay"), 0600)
	short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	started := time.Now()
	_, err = reader.Read(short)
	if err == nil || time.Since(started) > time.Second {
		t.Fatal("read deadline not bounded", err, time.Since(started))
	}
	// A listener on a non-loopback address on this same host must be absent.
	host, port, _ := net.SplitHostPort(addr)
	_ = host
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		ip, _, _ := net.ParseCIDR(address.String())
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), port), 100*time.Millisecond)
			if err == nil {
				conn.Close()
				t.Fatal("mock exposed on non-loopback interface", ip)
			}
		}
	}
}
