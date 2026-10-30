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
