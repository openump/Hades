//go:build plc4x

package gateway

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeTestPKI creates a throwaway CA with a loopback server and a client
// certificate for the mTLS ingress fake.
func writeTestPKI(t *testing.T, dir string) *tls.Config {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hades-test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	issue := func(name string, serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{usage}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, _ := x509.MarshalECPrivateKey(key)
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		os.WriteFile(filepath.Join(dir, name+".crt"), certPEM, 0o600)
		os.WriteFile(filepath.Join(dir, name+".key"), keyPEM, 0o600)
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatal(err)
		}
		return pair
	}
	os.WriteFile(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600)
	server := issue("server", 2, x509.ExtKeyUsageServerAuth)
	issue("gateway", 3, x509.ExtKeyUsageClientAuth)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{Certificates: []tls.Certificate{server}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13}
}

func buildGateway(t *testing.T, tags ...string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "hades-gateway")
	args := append([]string{"build"}, tags...)
	args = append(args, "-o", binary, "../../cmd/hades-gateway")
	if output, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		t.Fatalf("build hades-gateway %v: %v\n%s", tags, err, output)
	}
	return binary
}

func TestGatedGatewayBinaryPollsS7SimulatorIntoIngress(t *testing.T) {
	requireSimulators(t)
	dir := t.TempDir()
	serverTLS := writeTestPKI(t, dir)
	var mu sync.Mutex
	var envelopes []map[string]any
	ingress := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope map[string]any
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil || r.URL.Path != "/observations" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		envelopes = append(envelopes, envelope)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"acceptance":"accepted_broker_ack"}`))
	}))
	ingress.TLS = serverTLS
	ingress.StartTLS()
	defer ingress.Close()

	port := freePort(t, "tcp")
	sim, _ := pythonFixture(t, "s7_server.py", port)
	source := fmt.Sprintf("s7://127.0.0.1:%d", port)
	run := func(binary string, extra ...string) (string, error) {
		args := append([]string{"-lab", "-root", "../..", "-config", "../../examples/site/plc4x-s7-lab.metadata.json",
			"-ingress", ingress.URL + "/observations", "-count", "3",
			"-tls-ca", filepath.Join(dir, "ca.crt"), "-tls-cert", filepath.Join(dir, "gateway.crt"), "-tls-key", filepath.Join(dir, "gateway.key")}, extra...)
		output, err := exec.Command(binary, args...).CombinedOutput()
		return string(output), err
	}

	untagged := buildGateway(t)
	if output, err := run(untagged, "-source", source, "-acquisition", "../../examples/gateway/plc4x-s7-lab.acquisition.json"); err == nil || !strings.Contains(output, "-tags plc4x") {
		t.Fatalf("untagged gateway must refuse plc4go contexts: err=%v\n%s", err, output)
	}
	tagged := buildGateway(t, "-tags", "plc4x")
	if output, err := run(tagged, "-source", "s7://10.0.0.5:102", "-acquisition", "../../examples/gateway/plc4x-s7-lab.acquisition.json"); err == nil || !strings.Contains(output, "site approval is pending") {
		t.Fatalf("gated gateway must refuse non-loopback endpoints: err=%v\n%s", err, output)
	}
	if output, err := run(tagged, "-source", source); err == nil || !strings.Contains(output, "-acquisition") {
		t.Fatalf("gated gateway must require an acquisition map: err=%v\n%s", err, output)
	}
	output, err := run(tagged, "-source", source, "-acquisition", "../../examples/gateway/plc4x-s7-lab.acquisition.json")
	if err != nil {
		t.Fatalf("gated plc4go gateway: %v\n%s", err, output)
	}
	if _, _, writes := sim.ledgerCounts(); writes != 0 {
		t.Fatalf("%d write services reached the S7 controller", writes)
	}
	mu.Lock()
	defer mu.Unlock()
	// Poll 1 reports both signals; the 0.5 Cel deadband suppresses the unchanged
	// temperature on polls 2 and 3, while the counter has no deadband.
	if len(envelopes) != 4 {
		t.Fatalf("expected 4 admitted envelopes (2 + 1 + 1), got %d\n%s", len(envelopes), output)
	}
	seen := map[string]float64{}
	for _, envelope := range envelopes {
		source := envelope["source"].(map[string]any)
		if source["protocol"] != "plc4go" || source["gateway_id"] != "lab-plc4x-01" {
			t.Fatal(envelope)
		}
		for name, raw := range envelope["signals"].(map[string]any) {
			signal := raw.(map[string]any)
			if signal["quality"] != "good" {
				t.Fatal(envelope)
			}
			seen[name] = signal["value"].(float64)
		}
	}
	if !sameNumber(seen["motor_temperature"], 3.25) || !sameNumber(seen["cycle_counter"], 4242) {
		t.Fatal(seen)
	}
}
