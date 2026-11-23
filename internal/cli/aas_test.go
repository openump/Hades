package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Hades-045 boundary commands, end to end through the multicall binary:
// import a tag table, reconcile the result with hades check, export it to AAS,
// import that AAS back and get the same catalog files.
func TestImportCheckExportRoundTrip(t *testing.T) {
	hades, _ := build(t)
	dir := t.TempDir()
	run := func(args ...string) result {
		t.Helper()
		r := invoke(t, "hades", hades, args...)
		if r.code != 0 {
			t.Fatalf("hades %s: exit %d\n%s", strings.Join(args, " "), r.code, r.stderr)
		}
		return r
	}
	common := []string{"-root", root, "-revision", "3", "-published-at", "2026-11-23T10:00:00Z", "-gateway-id", "plc-01", "-protocol", "s7"}
	run(append([]string{"import", "-from-csv", filepath.Join(root, "examples/aas/tags.csv"), "-out-dir", filepath.Join(dir, "csv")}, common...)...)
	meta := filepath.Join(dir, "csv/acme.berlin.press-04.metadata.json")
	mapping := filepath.Join(dir, "csv/acme.berlin.press-04.semantic-mapping.json")
	if r := run("check", "-root", root, "-kind", "semantic-mapping", "-file", mapping, "-metadata", meta); !strings.Contains(r.stdout, "reconciles") {
		t.Fatalf("check: %q", r.stdout)
	}
	// A mapping does not reconcile with another asset's metadata.
	other := filepath.Join(dir, "csv/acme.berlin.pump-02.metadata.json")
	if r := invoke(t, "hades", hades, "check", "-root", root, "-kind", "semantic-mapping", "-file", mapping, "-metadata", other); r.code == 0 {
		t.Fatal("mapping reconciled with another asset's metadata")
	}
	env := filepath.Join(dir, "press-04.aas.json")
	run("aas", "export", "-root", root, "-metadata", meta, "-mapping", mapping, "-out", env)
	if r := invoke(t, "hades", hades, "aas", "export", "-root", root, "-metadata", meta, "-mapping", mapping, "-out", env); r.code == 0 || !strings.Contains(r.stderr, "-force") {
		t.Fatalf("export overwrote %s: %+v", env, r)
	}
	run(append([]string{"import", "-from-aas", env, "-out-dir", filepath.Join(dir, "aas"),
		"-enterprise", "acme", "-site", "berlin", "-area", "assembly", "-line", "line-01"}, common...)...)
	for _, f := range []string{"acme.berlin.press-04.metadata.json", "acme.berlin.press-04.semantic-mapping.json"} {
		want, _ := os.ReadFile(filepath.Join(dir, "csv", f))
		got, _ := os.ReadFile(filepath.Join(dir, "aas", f))
		if f == "acme.berlin.press-04.semantic-mapping.json" {
			// The export assigned AAS identifiers; the tag table had none.
			if !bytes.Contains(got, []byte(`"aas_id": "urn:openump:aas:acme:berlin:press-04"`)) {
				t.Fatalf("binding missing:\n%s", got)
			}
			continue
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("%s changed through AAS:\n%s\nvs\n%s", f, want, got)
		}
	}
}

func TestImportFlagErrors(t *testing.T) {
	hades, _ := build(t)
	csv := filepath.Join(root, "examples/aas/tags.csv")
	out := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"import", "-out-dir", out}, "exactly one of -from-csv or -from-aas"},
		{[]string{"import", "-from-csv", csv, "-from-aas", csv, "-out-dir", out}, "exactly one"},
		{[]string{"import", "-from-csv", csv, "-out-dir", out, "-revision", "1", "-gateway-id", "g", "-protocol", "p", "-site", "x"}, "-site apply to -from-aas"},
		{[]string{"import", "-from-csv", csv, "-out-dir", out, "-gateway-id", "g", "-protocol", "p"}, "-revision must be at least 1"},
		{[]string{"import", "-from-csv", csv, "-out-dir", out, "-revision", "1"}, "-gateway-id and -protocol are required"},
		{[]string{"import", "-from-csv", csv, "-out-dir", out, "-revision", "1", "-gateway-id", "g", "-protocol", "p", "-published-at", "yesterday"}, "-published-at"},
		{[]string{"aas"}, "usage: hades aas export"},
		{[]string{"aas", "export", "-metadata", csv}, "-metadata and -mapping are required"},
	} {
		r := invoke(t, "hades", hades, append(tc.args, "-root", root)...)
		if r.code == 0 || !strings.Contains(r.stderr, tc.want) {
			t.Errorf("%v: want %q, got exit %d %q", tc.args, tc.want, r.code, r.stderr)
		}
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatalf("a failed import wrote %d file(s)", len(entries))
	}
}
