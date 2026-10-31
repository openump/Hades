package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/infer"
	"github.com/openump/Hades/internal/mcp"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/supervisor"
	"github.com/openump/Hades/internal/transport"
)

// TestMCPProposalRoutesThroughRealActuator runs the deployed hades-actuator
// binary (supervised, shadow mode: gates evaluated, no PLC writes) on a real
// mTLS broker. A hades-mcp proposal travels the production path:
// mcp.Proposer -> signed command on MQTT -> actuator gates -> signed receipt
// -> mcp.VerifyReceipt. An L1 proposal must come back AWAITING_OPERATOR_CONFIRMATION.
func TestMCPProposalRoutesThroughRealActuator(t *testing.T) {
	broker, pki := os.Getenv("HADES_MCP_TEST_BROKER"), os.Getenv("HADES_MCP_TEST_PKI")
	if broker == "" || pki == "" {
		t.Skip("explicit disposable mTLS broker required")
	}
	root, _ := filepath.Abs("../..")
	dir, err := os.MkdirTemp("/tmp", "h34-mcp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	write := func(name string, raw []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	// Keys: the hades-mcp origin (signs proposals) and the actuator (signs receipts).
	mcpKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("m", ed25519.SeedSize)))
	actKey := ed25519.NewKeyFromSeed([]byte(strings.Repeat("a", ed25519.SeedSize)))
	// The site allowlists the hades-mcp origin for press-04 at an L1 ceiling.
	raw, _ := os.ReadFile(filepath.Join(root, "examples/contracts/control/actuation-policy.json"))
	var policy map[string]any
	json.Unmarshal(raw, &policy)
	asset := policy["assets"].([]any)[0].(map[string]any)
	asset["allowed_origins"] = []any{map[string]any{"actor_id": "hades-mcp", "key_id": "hades-mcp-key"}}
	policyRaw, _ := json.Marshal(policy)
	policyPath := write("policy.json", policyRaw)
	operatorKeys, _ := json.Marshal(map[string]string{"hades-mcp-key": base64.StdEncoding.EncodeToString(mcpKey.Public().(ed25519.PublicKey))})
	keysPath := write("operator-keys.json", operatorKeys)
	receiptKeyPath := write("receipt-key", []byte(base64.StdEncoding.EncodeToString(actKey)))

	// Start the real actuator binary, supervised, in shadow mode.
	binary := filepath.Join(dir, "hades-actuator")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hades-actuator")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	metadata := filepath.Join(root, "examples/site/metadata.json")
	state := filepath.Join(dir, "actuator")
	args := []string{"-state", state, "-root", root, "-config", metadata, "-policy", policyPath, "-keys", keysPath, "-receipt-key", receiptKeyPath, "-receipt-key-id", "mcp-test-actuator-key", "-broker", broker,
		"-tls-ca", filepath.Join(pki, "ca.crt"), "-tls-cert", filepath.Join(pki, "hades-actuator.crt"), "-tls-key", filepath.Join(pki, "hades-actuator.key")}
	binRaw, _ := os.ReadFile(binary)
	cfg := supervisor.Config{Revision: "actuator-for-mcp", Binary: binary, BinarySHA256: fmt.Sprintf("%x", sha256.Sum256(binRaw)), Args: args, Directory: root, State: filepath.Join(dir, "service"), Ready: "actuator_ready", Files: map[string]string{}, WritablePaths: []string{state}, LogBytes: 65536}
	files := []string{policyPath, keysPath, receiptKeyPath, metadata, filepath.Join(pki, "ca.crt"), filepath.Join(pki, "hades-actuator.crt"), filepath.Join(pki, "hades-actuator.key")}
	schemas, _ := filepath.Glob(filepath.Join(root, "schemas/v1/*.json"))
	for _, p := range append(files, schemas...) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Files[p] = fmt.Sprintf("%x", sha256.Sum256(b))
	}
	definition, _ := json.Marshal(cfg)
	servicePath := write("service.json", definition)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, servicePath, supervisor.Digest(cfg)) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(8 * time.Second):
			t.Error("supervised actuator failed to stop")
		}
	}()
	for i := 0; i < 300; i++ {
		if status, err := supervisor.Probe(context.Background(), cfg); err == nil && status.Ready {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// hades-mcp side: load context and connect with the hades-mcp identity.
	site, err := sample.LoadContext(root, metadata)
	if err != nil {
		t.Fatal(err)
	}
	site.TLS, err = transport.Client(transport.Paths{CA: filepath.Join(pki, "ca.crt"), Cert: filepath.Join(pki, "hades-mcp.crt"), Key: filepath.Join(pki, "hades-mcp.key")})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contracts.New(os.DirFS(filepath.Join(root, "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	receipts := newReceiptBox()
	cache := mcp.NewLatestCache(validator, site.Registry)
	topics := []paho.SubscribeOptions{{Topic: site.Prefix + "command_receipt/+", QoS: 1}}
	manager, err := connectBroker(context.Background(), broker, site, topics, cache, receipts)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Disconnect(context.Background())
	proposer := &mcp.Proposer{
		Validator: validator, Registry: site.Registry, Policy: policyRaw,
		ActorID: "hades-mcp", KeyID: "hades-mcp-key", Key: mcpKey, TTL: 5 * time.Second,
		TopicFor: func(_, parameter string) (string, error) { return site.Prefix + "command/" + parameter, nil },
		Now:      func() time.Time { return time.Now().UTC() }, NewID: infer.UUIDv7,
	}
	proposal, err := proposer.Build(mcp.ProposalRequest{Asset: "urn:openump:acme:berlin:press-04", Parameter: "pressure_setpoint", Value: 5})
	if err != nil {
		t.Fatal(err)
	}
	// The actuator's client ID derives from its receipt key ID, so a unique ID
	// gives this test its own MQTT session, isolated from other actuator tests.
	trust := map[string]ed25519.PublicKey{"mcp-test-actuator-key": actKey.Public().(ed25519.PublicKey)}
	receipt, err := publishAndAwait(context.Background(), manager, receipts, proposal, validator, trust)
	if err != nil {
		events, _ := os.ReadFile(filepath.Join(cfg.State, "events.log"))
		t.Fatalf("no verified receipt from the real actuator: %v\n%s", err, events)
	}
	// No AI proposal ever executes. In shadow mode (no write pilot) the actuator
	// forces L0, giving SHADOW_LOGGED; with the pilot on, an unconfirmed L1 gives
	// AWAITING_OPERATOR_CONFIRMATION. Never EXECUTED.
	if state := receipt["state"]; state != "SHADOW_LOGGED" && state != "AWAITING_OPERATOR_CONFIRMATION" {
		t.Fatalf("an MCP proposal must never execute: %v", state)
	}
	t.Logf("real actuator receipt for MCP proposal %s: %v", proposal.CommandID, receipt["state"])

	// Negative: the hades-mcp identity cannot publish device telemetry.
	c, done2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer done2()
	ack, err := manager.Publish(c, &paho.Publish{Topic: site.Prefix + "telemetry", Payload: []byte(`{}`), QoS: 1})
	if err == nil && (ack == nil || ack.ReasonCode < 0x80) {
		t.Fatal("hades-mcp must not be able to publish device telemetry")
	}
}
