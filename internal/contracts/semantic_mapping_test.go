package contracts

import (
	"encoding/json"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// bundle is the repository schema set minus the named schema files.
func bundle(t *testing.T, without ...string) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	skip := map[string]bool{}
	for _, w := range without {
		skip["v1/"+w+".schema.json"] = true
	}
	err := fs.WalkDir(os.DirFS("../../schemas"), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || skip[path] {
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

// TestPriorSiteBundlesStayValid keeps every earlier signed site bundle valid:
// each compiles without semantic-mapping and refuses that kind.
func TestPriorSiteBundlesStayValid(t *testing.T) {
	mapping, _ := os.ReadFile("../../examples/contracts/semantic-mapping/semantic-mapping.json")
	metadata, _ := os.ReadFile("../../examples/site/metadata.json")
	for name, without := range map[string][]string{
		"seven-schema (telemetry + control)":      {"model-manifest", "inference-event", "derived-value", "semantic-mapping"},
		"nine-schema (+ Edge AI, Hades-035)":      {"derived-value", "semantic-mapping"},
		"ten-schema (+ derived-value, Hades-043)": {"semantic-mapping"},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := New(bundle(t, without...))
			if err != nil {
				t.Fatal("a prior bundle must still compile:", err)
			}
			if _, err := v.Validate("metadata", metadata); err != nil {
				t.Fatal("a prior bundle must still validate metadata:", err)
			}
			if _, err := v.Validate("semantic-mapping", mapping); err == nil {
				t.Fatal("a bundle without the schema must refuse semantic-mapping")
			}
		})
	}
}

func TestSemanticMappingReconciliation(t *testing.T) {
	v, err := New(os.DirFS("../../schemas"))
	if err != nil {
		t.Fatal(err)
	}
	rawMapping, _ := os.ReadFile("../../examples/contracts/semantic-mapping/semantic-mapping.json")
	rawMetadata, _ := os.ReadFile("../../examples/site/metadata.json")
	mapping, err := v.Validate("semantic-mapping", rawMapping)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := v.Validate("metadata", rawMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := ReconcileSemanticMapping(mapping, metadata); err != nil {
		t.Fatal("the example pair must reconcile:", err)
	}
	for _, other := range []any{json.Number("2"), 2.0, nil, "1"} {
		metadata["metadata_revision"] = other
		if err := ReconcileSemanticMapping(mapping, metadata); err == nil || !strings.Contains(err.Error(), "metadata_revision") {
			t.Fatalf("revision %#v must be refused, not accepted or panic: %v", other, err)
		}
	}
	metadata["metadata_revision"] = 1.0 // plain json.Unmarshal form of the same revision
	if err := ReconcileSemanticMapping(mapping, metadata); err != nil {
		t.Fatal("a float64 revision equal to the mapping's must reconcile:", err)
	}
}

func TestCanonicalSemanticID(t *testing.T) {
	if got := CanonicalSemanticID("irdi:0173-1#02-AAB991#008"); got != "0173-1#02-AAB991#008" {
		t.Fatal(got)
	}
	if got := CanonicalSemanticID("https://example.com/x"); got != "https://example.com/x" {
		t.Fatal(got)
	}
}
