package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openump/Hades/internal/mcp"
)

// pkiFor issues a throwaway CA, a loopback server certificate and client
// certificates for the given common names.
func pkiFor(t *testing.T, clients ...string) (server tls.Certificate, pool *x509.CertPool, certs map[string]tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	pool = x509.NewCertPool()
	pool.AddCert(ca)
	issue := func(cn string, serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	server = issue("hades-mcp-server", 2, x509.ExtKeyUsageServerAuth)
	certs = map[string]tls.Certificate{}
	for i, cn := range clients {
		certs[cn] = issue(cn, int64(10+i), x509.ExtKeyUsageClientAuth)
	}
	return server, pool, certs
}

// rpc posts one JSON-RPC message as a client and returns status, session id
// and body.
func rpc(t *testing.T, url string, client *http.Client, session string, body string) (int, string, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Mcp-Session-Id"), string(raw)
}

// TestReadOnlyClientCannotHijackProposerSession: a read-only certificate
// presenting a proposer's Mcp-Session-Id is refused before the MCP layer.
func TestReadOnlyClientCannotHijackProposerSession(t *testing.T) {
	serverCert, pool, certs := pkiFor(t, "agent-proposer", "agent-readonly", "stranger")
	roles := map[string]mcp.Role{"agent-proposer": mcp.RoleProposer, "agent-readonly": mcp.RoleReadOnly}
	build := func(role mcp.Role, caller string) *sdk.Server {
		s := sdk.NewServer(&sdk.Implementation{Name: "hades-mcp", Version: "test"}, nil)
		if role == mcp.RoleProposer {
			sdk.AddTool(s, &sdk.Tool{Name: "propose_command"}, func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, struct{}, error) {
				return nil, struct{}{}, nil
			})
		}
		sdk.AddTool(s, &sdk.Tool{Name: "list_assets"}, func(_ context.Context, _ *sdk.CallToolRequest, _ struct{}) (*sdk.CallToolResult, struct{}, error) {
			return nil, struct{}{}, nil
		})
		return s
	}
	ts := httptest.NewUnstartedServer(newHTTPHandler(roles, build))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}
	ts.StartTLS()
	defer ts.Close()
	clientFor := func(cn string) *http.Client {
		return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{certs[cn]}, MinVersion: tls.VersionTLS13}}}
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`

	code, proposerSession, _ := rpc(t, ts.URL, clientFor("agent-proposer"), "", initialize)
	if code != http.StatusOK || proposerSession == "" {
		t.Fatalf("proposer initialize: %d session=%q", code, proposerSession)
	}
	// The attack: a read-only certificate replays the proposer's session id.
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	if code, _, body := rpc(t, ts.URL, clientFor("agent-readonly"), proposerSession, list); code != http.StatusForbidden {
		t.Fatalf("read-only client reused the proposer session: %d %s", code, body)
	}
	// A client with no role is refused outright.
	if code, _, _ := rpc(t, ts.URL, clientFor("stranger"), "", initialize); code != http.StatusForbidden {
		t.Fatalf("an unlisted client must be refused, got %d", code)
	}
	// An id this server never issued is refused too.
	if code, _, _ := rpc(t, ts.URL, clientFor("agent-readonly"), "forged-session-id", list); code != http.StatusForbidden {
		t.Fatalf("a forged session id must be refused, got %d", code)
	}
	// The legitimate owner keeps using its own session.
	if code, _, body := rpc(t, ts.URL, clientFor("agent-proposer"), proposerSession, list); code != http.StatusOK || !strings.Contains(body, "propose_command") {
		t.Fatalf("the owner must keep its session: %d %s", code, body)
	}
}
