package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openump/Hades/internal/sample"
)

func TestAcquisitionConfigAppliesDeadbandsSubscribeAndTags(t *testing.T) {
	cfg, err := LoadAcquisitionConfig("../../examples/gateway/opcua-mock.acquisition.json")
	if err != nil {
		t.Fatal(err)
	}
	site, err := sample.LoadContext("../..", "../../examples/site/opcua-mock.metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var options DriverOptions
	var factory FactoryOptions
	if err := cfg.Apply(site.Metadata["measurements"].(map[string]any), &options, &factory); err != nil {
		t.Fatal(err)
	}
	if !options.Subscribe || options.Deadbands["motor_temperature"] != (DeadbandConfig{Epsilon: 0.5, MaxSilenceInterval: time.Minute}) {
		t.Fatal(options)
	}
	if factory.PLC4GoTags != nil {
		t.Fatal("expected no tag addresses for the OPC UA example", factory.PLC4GoTags)
	}

	tagged := &AcquisitionConfig{Signals: map[string]AcquisitionSignal{"motor_temperature": {Address: "%DB1:0:REAL"}}}
	if err := tagged.Apply(map[string]any{"motor_temperature": map[string]any{}}, &options, &factory); err != nil || factory.PLC4GoTags["motor_temperature"] != "%DB1:0:REAL" {
		t.Fatal(factory.PLC4GoTags, err)
	}
	if err := tagged.Apply(map[string]any{"other": map[string]any{}}, &options, &factory); err == nil || !strings.Contains(err.Error(), "unknown measurement") {
		t.Fatal(err)
	}
}

func TestAcquisitionConfigRejectsInvalidSettings(t *testing.T) {
	cases := map[string]string{
		"unknown field":   `{"signals":{"a":{"deadbnd":{}}}}`,
		"negative eps":    `{"signals":{"a":{"deadband":{"epsilon":-1,"max_silence_ms":60000}}}}`,
		"short heartbeat": `{"signals":{"a":{"deadband":{"epsilon":1,"max_silence_ms":10}}}}`,
		"trailing data":   `{"signals":{}} {}`,
	}
	dir := t.TempDir()
	for name, body := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadAcquisitionConfig(path); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

func TestLabPLC4GoEndpointRequiresLiteralLoopback(t *testing.T) {
	for _, endpoint := range []string{
		"s7://127.0.0.1:10102?cotp.remote-rack=0&cotp.remote-slot=1",
		"eip://127.0.0.1:44818",
		"ads://[::1]:48898?target-ams-net-id=127.0.0.1.1.1",
		"bacnet-ip://127.0.0.1:47808",
		"modbus-tcp://127.0.0.1:1502",
		"logix://127.0.0.1:44818?backplane=1&slot=0",
		"bacnet-ip://127.0.0.1:47809?local-port=47900",
	} {
		if err := ValidateLabPLC4GoEndpoint(endpoint); err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
	}
	for _, endpoint := range []string{
		"s7://10.0.0.5", "s7://localhost:102", "eip://plc.example:44818",
		"modbus-tcp://user@127.0.0.1:502", "opc.tcp://127.0.0.1:4840", "s7://127.0.0.1/path",
		// Routing options could reach past the loopback peer.
		"logix://127.0.0.1:44818?communication-path=1,0,2,10.0.0.5",
		"eip://127.0.0.1:44818?communicationPath=1,0",
		"bacnet-ip://127.0.0.1:47808?remote-network=5&remote-address=10.0.0.5",
		"bacnet-ip://127.0.0.1:47808?foreign-device-bbmd=10.0.0.1",
		"bacnet-ip://127.0.0.1:47808?static-devices=10.0.0.5",
		"s7://127.0.0.1:102?unknown-option=1",
	} {
		if err := ValidateLabPLC4GoEndpoint(endpoint); err == nil {
			t.Fatalf("expected %q to be rejected by the lab gate", endpoint)
		}
	}
}

func TestPLC4XGatewayConnectorMatchesBuildTag(t *testing.T) {
	connector, err := NewPLC4XGatewayConnector()
	if PLC4XAvailable != (err == nil) || PLC4XAvailable != (connector != nil) {
		t.Fatal(PLC4XAvailable, connector, err)
	}
	if !PLC4XAvailable && !strings.Contains(err.Error(), "-tags plc4x") {
		t.Fatal(err)
	}
}
