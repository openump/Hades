package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/infer"
	"github.com/openump/Hades/internal/supervisor"
	"github.com/openump/Hades/internal/transport"
)

const pressTopic = "openump/v1/acme/berlin/assembly/line-01/press-04/"

// TestSupervisedInferenceOverMTLSBroker runs hades-infer under the hades-service
// supervisor against a disposable mTLS broker: drift telemetry in, a
// contract-valid inference event out, and the sidecar's resident memory
// recorded against the Tier 2 budget. scripts/smoke-security provides the broker.
func TestSupervisedInferenceOverMTLSBroker(t *testing.T) {
	broker, pki := os.Getenv("HADES_INFER_TEST_BROKER"), os.Getenv("HADES_INFER_TEST_PKI")
	if broker == "" || pki == "" {
		t.Skip("explicit disposable mTLS broker required")
	}
	root, _ := filepath.Abs("../..")
	dir, err := os.MkdirTemp("/tmp", "h36-supervised-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	binary := filepath.Join(dir, "hades-infer")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hades-infer")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}

	// A signed statistical_v1 bundle: EWMA drift on motor temperature.
	key := ed25519.NewKeyFromSeed([]byte(strings.Repeat("h", ed25519.SeedSize)))
	artifact := []byte(`{"detectors":[{"kind":"ewma_drift","measurement":"motor_temperature","alpha":0.3,"scale":1}],"soft_sensors":[{"kind":"ewma","measurement":"motor_temperature","output":"motor_temperature_trend","alpha":0.3}]}`)
	bundle := filepath.Join(dir, "bundle")
	os.Mkdir(bundle, 0o700)
	manifest := map[string]any{
		"schema_version": "1.0", "manifest_id": "019a1234-5678-7000-8000-000000000360",
		"model_id": "press-04-thermal", "model_version": "1.0.0", "created_at_ms": 1792152000000,
		"runtime_type":    "statistical_v1",
		"artifact_sha256": func() string { s := sha256.Sum256(artifact); return hex.EncodeToString(s[:]) }(),
		"signature":       map[string]any{"algorithm": "Ed25519", "canonicalization": "RFC8785", "signer_id": "ml-release", "key_id": "ml-release-2026", "value": strings.Repeat("A", 86) + "=="},
		"inputs": map[string]any{
			"bindings":            []any{map[string]any{"asset": "urn:openump:acme:berlin:press-04", "measurement": "motor_temperature", "unit": "Cel"}},
			"window_size_samples": 10, "window_duration_ms": 60000, "required_quality": "good", "min_good_ratio": 0.9,
		},
		"outputs": []any{
			map[string]any{"kind": "inference-event", "asset": "urn:openump:acme:berlin:press-04", "inference_type": "thermal_drift", "severity": map[string]any{"warning_score": 0.5, "critical_score": 0.9}},
			map[string]any{"kind": "telemetry", "asset": "urn:openump:acme:berlin:press-04", "measurement": "motor_temperature_trend", "unit": "Cel"},
		},
		"budgets": map[string]any{"max_memory_mb": 64, "max_inference_latency_ms": 500, "max_consecutive_failures": 3, "rollback_to_version": nil},
	}
	encoded, _ := json.Marshal(manifest)
	signed, err := infer.SignManifest(encoded, key)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(bundle, infer.ManifestFile), signed, 0o600)
	os.WriteFile(filepath.Join(bundle, infer.ArtifactFile), artifact, 0o600)
	keyring, _ := json.Marshal(map[string]string{"ml-release-2026": base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))})
	keys := filepath.Join(dir, "keys.json")
	os.WriteFile(keys, keyring, 0o600)

	metadata := filepath.Join(root, "examples/site/metadata.json")
	args := []string{"-root", root, "-config", metadata, "-bundle", bundle, "-keys", keys, "-broker", broker, "-interval", "200ms",
		"-tls-ca", filepath.Join(pki, "ca.crt"), "-tls-cert", filepath.Join(pki, "hades-infer.crt"), "-tls-key", filepath.Join(pki, "hades-infer.key")}
	raw, _ := os.ReadFile(binary)
	cfg := supervisor.Config{Revision: "infer-pilot", Binary: binary, BinarySHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), Args: args, Directory: root, State: filepath.Join(dir, "service"), Ready: "infer_ready", Files: map[string]string{}, LogBytes: 65536}
	files := []string{keys, metadata, filepath.Join(bundle, infer.ManifestFile), filepath.Join(bundle, infer.ArtifactFile), filepath.Join(pki, "ca.crt"), filepath.Join(pki, "hades-infer.crt"), filepath.Join(pki, "hades-infer.key")}
	schemas, _ := filepath.Glob(filepath.Join(root, "schemas/v1/*.json"))
	for _, p := range append(files, schemas...) {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Files[p] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	definition, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "service.json")
	os.WriteFile(path, definition, 0o600)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, path, supervisor.Digest(cfg)) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("supervised hades-infer failed to stop")
		}
	}()
	var status supervisor.Status
	for i := 0; i < 300; i++ {
		if status, err = supervisor.Probe(context.Background(), cfg); err == nil && status.Ready {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !status.Ready {
		events, _ := os.ReadFile(filepath.Join(cfg.State, "events.log"))
		t.Fatalf("hades-infer never became ready: %s", events)
	}

	// Subscribe as the opted-in consumer, then publish drift as the normalizer.
	events, derivedValues, telemetryEchoes := make(chan []byte, 8), make(chan []byte, 8), make(chan string, 64)
	consumer := mqttClient(t, broker, pki, "consumer-primary", func(p *paho.Publish) {
		switch {
		case strings.Contains(p.Topic, "/event/"):
			events <- p.Payload
		case strings.Contains(p.Topic, "/derived/"):
			derivedValues <- p.Payload
		}
	})
	subctx, subdone := context.WithTimeout(context.Background(), 3*time.Second)
	if _, err := consumer.Subscribe(subctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: pressTopic + "event/+", QoS: 1}, {Topic: pressTopic + "derived/+", QoS: 1}}}); err != nil {
		t.Fatal(err)
	}
	subdone()
	// history-adapter consumes device telemetry into history; a derived value
	// must never reach it.
	history := mqttClient(t, broker, pki, "history-adapter", func(p *paho.Publish) {
		select {
		case telemetryEchoes <- string(p.Payload):
		default:
		}
	})
	hctx, hdone := context.WithTimeout(context.Background(), 3*time.Second)
	if _, err := history.Subscribe(hctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: pressTopic + "telemetry", QoS: 1}}}); err != nil {
		t.Fatal(err)
	}
	hdone()
	normalizer := mqttClient(t, broker, pki, "normalizer", nil)
	base := time.Now().UTC().Add(-30 * time.Second).Truncate(time.Second)
	for i := 0; i < 12; i++ {
		at := base.Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05.000Z")
		payload := fmt.Sprintf(`{"schema_version":"1.0","message_id":"019a1234-5678-4000-8000-%012d","asset_id":"urn:openump:acme:berlin:press-04","metadata_revision":1,"observed_at":"%s","ingested_at":"%s","source":{"gateway_id":"simulator-01","protocol":"simulator","boot_id":"019a1234-5678-4000-8000-000000000999","sequence":%d,"clock_origin":"gateway","clock_uncertainty_ms":null},"measurements":{"motor_temperature":{"value":%s,"unit":"Cel","quality":"good"}}}`,
			i, at, at, i, strconv.FormatFloat(70+0.6*float64(i), 'f', 2, 64))
		pubctx, pubdone := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := normalizer.Publish(pubctx, &paho.Publish{Topic: pressTopic + "telemetry", Payload: []byte(payload), QoS: 1})
		pubdone()
		if err != nil {
			t.Fatal(err)
		}
	}
	validator, err := contracts.New(os.DirFS(filepath.Join(root, "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-events:
		event, err := validator.Validate("inference-event", payload)
		if err != nil {
			t.Fatalf("published event fails the contract: %v\n%s", err, payload)
		}
		if event["model_version"] != "1.0.0" || event["inference_type"] != "thermal_drift" || event["severity"] == "info" {
			t.Fatalf("expected a drift alert with provenance: %s", payload)
		}
	case <-time.After(15 * time.Second):
		log, _ := os.ReadFile(filepath.Join(cfg.State, "events.log"))
		t.Fatalf("no inference event received: %s", log)
	}

	select {
	case payload := <-derivedValues:
		var value map[string]any
		json.Unmarshal(payload, &value)
		if value["measurement"] != "motor_temperature_trend" || value["model_version"] != "1.0.0" {
			t.Fatalf("expected a provenance-tagged derived value: %s", payload)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no derived soft-sensor value received")
	}
	// Soft sensors must never appear on the device telemetry topic.
	for drained := false; !drained; {
		select {
		case payload := <-telemetryEchoes:
			if strings.Contains(payload, "motor_temperature_trend") || strings.Contains(payload, "hades-infer") {
				t.Fatalf("a derived value leaked onto device telemetry: %s", payload)
			}
		default:
			drained = true
		}
	}

	if rss := residentMB(t, status.ChildPID); rss > 0 {
		t.Logf("hades-infer resident memory: %.1f MB (Tier 2 budget for the whole edge stack: 250 MB)", rss)
		if rss > 64 {
			t.Fatalf("hades-infer resident memory %.1f MB exceeds the model's 64 MB budget", rss)
		}
	}
}

func mqttClient(t *testing.T, broker, pki, role string, receive func(*paho.Publish)) *autopaho.ConnectionManager {
	t.Helper()
	tls, err := transport.Client(transport.Paths{CA: filepath.Join(pki, "ca.crt"), Cert: filepath.Join(pki, role+".crt"), Key: filepath.Join(pki, role+".key")})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(broker)
	conf := autopaho.ClientConfig{ServerUrls: []*url.URL{u}, TlsCfg: tls, ConnectTimeout: time.Second, ClientConfig: paho.ClientConfig{ClientID: "h36-test-" + role, PacketTimeout: time.Second}}
	if receive != nil {
		conf.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(e paho.PublishReceived) (bool, error) { receive(e.Packet); return true, nil }}
	}
	m, err := autopaho.NewConnection(context.Background(), conf)
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

// residentMB returns the process's resident set size in MB, or 0 if the
// platform does not expose it to this test.
func residentMB(t *testing.T, pid int) float64 {
	t.Helper()
	if pid <= 0 {
		return 0
	}
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return 0
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				kb, _ := strconv.ParseFloat(strings.Fields(line)[1], 64)
				return kb / 1024
			}
		}
		return 0
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	kb, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return kb / 1024
}
