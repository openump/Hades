package contracts

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestInferenceFixtures(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../examples/contracts/inference/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Kind, Error string
		Payload           json.RawMessage
		Valid             bool
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			_, err := v.Validate(c.Kind, c.Payload)
			if (err == nil) != c.Valid {
				t.Fatal(err, c.Valid)
			}
			var failure *Error
			if !c.Valid && (!errors.As(err, &failure) || failure.Code != c.Error) {
				t.Fatalf("expected %q rejection, got %v", c.Error, err)
			}
		})
	}
}

func TestInferenceEventTopicBinding(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../examples/contracts/inference/inference-event.json")
	if err != nil {
		t.Fatal(err)
	}
	event, err := v.Validate("inference-event", raw)
	if err != nil {
		t.Fatal(err)
	}
	assets, err := os.ReadFile("../../examples/contracts/assets.json")
	if err != nil {
		t.Fatal(err)
	}
	var list []Asset
	if err := json.Unmarshal(assets, &list); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(list)
	if err != nil {
		t.Fatal(err)
	}
	base := "openump/v1/acme/berlin/assembly/line-01/press-04/"
	if err := registry.ValidateInferenceEventTopic(base+"event/vibration_anomaly", event); err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{
		base + "event/other_anomaly",           // type mismatch
		base + "event",                         // missing type
		base + "command/vibration_anomaly",     // control subtree
		base + "event/vibration_anomaly/extra", // too deep
		"openump/v1/acme/berlin/assembly/line-01/press-05/event/vibration_anomaly", // other asset
	} {
		if err := registry.ValidateInferenceEventTopic(topic, event); err == nil {
			t.Fatalf("expected %q to be rejected", topic)
		}
	}
}

// TestControlProfileStillCompilesWithoutEdgeAISchemas keeps existing
// seven-schema site bundles valid: the Edge AI schemas are additive.
func TestControlProfileStillCompilesWithoutEdgeAISchemas(t *testing.T) {
	files := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS("../../schemas"), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if path == "v1/model-manifest.schema.json" || path == "v1/inference-event.schema.json" {
			return nil
		}
		raw, err := os.ReadFile("../../schemas/" + path)
		files[path] = &fstest.MapFile{Data: raw}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := New(files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate("model-manifest", []byte(`{}`)); err == nil {
		t.Fatal("a seven-schema bundle must not accept Edge AI kinds")
	}
	command, _ := os.ReadFile("../../examples/contracts/control/command.json")
	if _, err := v.Validate("command", command); err != nil {
		t.Fatal(err)
	}
}

func schemaSubset(t *testing.T, drop ...string) fstest.MapFS {
	t.Helper()
	skip := map[string]bool{}
	for _, name := range drop {
		skip["v1/"+name+".schema.json"] = true
	}
	files := fstest.MapFS{}
	err := fs.WalkDir(os.DirFS("../../schemas"), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || skip[path] {
			return err
		}
		raw, err := os.ReadFile("../../schemas/" + path)
		files[path] = &fstest.MapFile{Data: raw}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestSchemaProfilesSelectedByPresentIDs(t *testing.T) {
	// Partial Edge AI rollout: events without manifests still compile.
	v, err := New(schemaSubset(t, "model-manifest"))
	if err != nil {
		t.Fatal(err)
	}
	event, _ := os.ReadFile("../../examples/contracts/inference/inference-event.json")
	if _, err := v.Validate("inference-event", event); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate("model-manifest", []byte(`{}`)); err == nil {
		t.Fatal("absent schema kind must be unsupported")
	}
	// Telemetry-only bundle remains valid.
	if _, err := New(schemaSubset(t, "command", "command-receipt", "actuation-policy", "model-manifest", "inference-event")); err != nil {
		t.Fatal(err)
	}
	// An incomplete control profile or telemetry core fails closed.
	for _, drop := range [][]string{{"command-receipt"}, {"status"}} {
		if _, err := New(schemaSubset(t, drop...)); err == nil {
			t.Fatalf("expected an incomplete profile without %v to fail", drop)
		}
	}
	// An unknown schema ID fails closed.
	files := schemaSubset(t)
	files["v1/extra.schema.json"] = &fstest.MapFile{Data: []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"urn:openump:schema:1.0:extra","type":"object"}`)}
	if _, err := New(files); err == nil {
		t.Fatal("expected an unknown schema ID to fail")
	}
}
