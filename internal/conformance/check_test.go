package conformance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryConformance(t *testing.T) {
	if err := Run("../..", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}
func TestBaselineRejectsDrift(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"schemas/v1", "examples/contracts/telemetry", "tests/conformance"} {
		os.MkdirAll(filepath.Join(root, dir), 0755)
	}
	path := filepath.Join(root, "schemas/v1/example.schema.json")
	os.WriteFile(path, []byte(`{"type":"object"}`), 0644)
	fixture := filepath.Join(root, "examples/contracts/telemetry/cases.json")
	os.WriteFile(fixture, []byte(`[]`), 0644)
	if err := WriteBaseline(root); err != nil {
		t.Fatal(err)
	}
	if err := CheckBaseline(root); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{path, fixture} {
		original, _ := os.ReadFile(file)
		os.WriteFile(file, append(original, ' '), 0644)
		if err := CheckBaseline(root); err == nil {
			t.Fatal("accepted drift", file)
		}
		os.WriteFile(file, original, 0644)
	}
	os.WriteFile(filepath.Join(root, "schemas/v1/added.schema.json"), []byte(`{}`), 0644)
	if err := CheckBaseline(root); err == nil {
		t.Fatal("accepted unreviewed file")
	}
}
