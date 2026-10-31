package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/actuator"
	"github.com/openump/Hades/internal/transport"
)

// TestInspectorShowsLiveTelemetryAndOnlyVerifiedReceipts runs the real
// hades-inspector binary on a disposable mTLS broker: telemetry published by
// the normalizer appears in the snapshot, a receipt signed by the trusted
// actuator key appears, and a forged receipt does not.
func TestInspectorShowsLiveTelemetryAndOnlyVerifiedReceipts(t *testing.T) {
	broker, pki := os.Getenv("HADES_INSPECTOR_TEST_BROKER"), os.Getenv("HADES_INSPECTOR_TEST_PKI")
	if broker == "" || pki == "" {
		t.Skip("explicit disposable mTLS broker required")
	}
	root, _ := filepath.Abs("../..")
	dir := t.TempDir()
	actKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("i", ed25519.SeedSize)))
	keyring, _ := json.Marshal(map[string]string{"actuator-key": base64.StdEncoding.EncodeToString(actKey.Public().(ed25519.PublicKey))})
	keys := filepath.Join(dir, "actuator-keys.json")
	os.WriteFile(keys, keyring, 0o600)
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	listener.Close()
	binary := filepath.Join(dir, "hades-inspector")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/hades-inspector").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	cmd := exec.Command(binary, "-root", root, "-config", filepath.Join(root, "examples/site/metadata.json"), "-broker", broker, "-listen", addr, "-actuator-keys", keys,
		"-certificates", filepath.Join(pki, "hades-inspector.crt"),
		"-tls-ca", filepath.Join(pki, "ca.crt"), "-tls-cert", filepath.Join(pki, "hades-inspector.crt"), "-tls-key", filepath.Join(pki, "hades-inspector.key"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	base := "http://" + addr
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if resp, err := http.Get(base + "/ui/api/snapshot"); err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inspector did not start")
		}
	}

	prefix := "openump/v1/acme/berlin/assembly/line-01/press-04/"
	normalizer := client(t, broker, pki, "normalizer")
	actuatorClient := client(t, broker, pki, "hades-actuator")
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	telemetry := fmt.Sprintf(`{"schema_version":"1.0","message_id":"019a1234-5678-4000-8000-000000000380","asset_id":"urn:openump:acme:berlin:press-04","metadata_revision":1,"observed_at":"%s","ingested_at":"%s","source":{"gateway_id":"simulator-01","protocol":"simulator","boot_id":"019a1234-5678-4000-8000-000000000381","sequence":1,"clock_origin":"gateway","clock_uncertainty_ms":null},"measurements":{"motor_temperature":{"value":71.25,"unit":"Cel","quality":"good"}}}`, now, now)
	publish(t, normalizer, prefix+"telemetry", []byte(telemetry))
	receipt := func(id string, key ed25519.PrivateKey) []byte {
		raw, _ := os.ReadFile(filepath.Join(root, "examples/contracts/control/command-receipt.json"))
		var r map[string]any
		json.Unmarshal(raw, &r)
		r["command_id"] = id
		r["signature"].(map[string]any)["key_id"] = "actuator-key"
		unsigned, _ := json.Marshal(r)
		signed, err := actuator.Sign(unsigned, key, true)
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	rogue := ed25519.NewKeyFromSeed([]byte(strings.Repeat("r", ed25519.SeedSize)))
	publish(t, actuatorClient, prefix+"command_receipt/pressure_setpoint", receipt("019a1234-5678-7000-8000-000000000382", actKey))
	publish(t, actuatorClient, prefix+"command_receipt/pressure_setpoint", receipt("019a1234-5678-7000-8000-000000000383", rogue))

	var snap struct {
		Assets []struct {
			Signals map[string]struct {
				Value   float64 `json:"value"`
				Quality string  `json:"quality"`
			} `json:"signals"`
		} `json:"assets"`
		Receipts []struct {
			CommandID string `json:"command_id"`
		} `json:"receipts"`
		Certificates []struct {
			Subject string `json:"subject"`
		} `json:"certificates"`
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		resp, err := http.Get(base + "/ui/api/snapshot")
		if err == nil {
			json.NewDecoder(resp.Body).Decode(&snap)
			resp.Body.Close()
		}
		if len(snap.Assets) == 1 && snap.Assets[0].Signals["motor_temperature"].Value == 71.25 && len(snap.Receipts) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot never showed live telemetry and the verified receipt: %+v", snap)
		}
	}
	if q := snap.Assets[0].Signals["motor_temperature"].Quality; q != "GOOD" {
		t.Fatal("expected GOOD quality:", q)
	}
	for _, r := range snap.Receipts {
		if r.CommandID == "019a1234-5678-7000-8000-000000000383" {
			t.Fatal("a receipt signed by an untrusted key must never be displayed")
		}
	}
	if len(snap.Certificates) != 1 || snap.Certificates[0].Subject != "hades-inspector" {
		t.Fatalf("certificate expiry not surfaced: %+v", snap.Certificates)
	}
	// The console itself refuses writes on the real listener too.
	req, _ := http.NewRequest(http.MethodPost, base+"/ui/api/snapshot", strings.NewReader("{}"))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatal("the running console must reject writes", resp, err)
	}
}

func client(t *testing.T, broker, pki, role string) *autopaho.ConnectionManager {
	t.Helper()
	tls, err := transport.Client(transport.Paths{CA: filepath.Join(pki, "ca.crt"), Cert: filepath.Join(pki, role+".crt"), Key: filepath.Join(pki, role+".key")})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(broker)
	m, err := autopaho.NewConnection(context.Background(), autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: tls, ConnectTimeout: time.Second, ClientConfig: paho.ClientConfig{ClientID: "h38-test-" + role, PacketTimeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.AwaitConnection(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Disconnect(context.Background()) })
	return m
}

func publish(t *testing.T, m *autopaho.ConnectionManager, topic string, payload []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := m.Publish(ctx, &paho.Publish{Topic: topic, Payload: payload, QoS: 1}); err != nil {
		t.Fatal(err)
	}
}
