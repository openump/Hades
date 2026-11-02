package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var root, _ = filepath.Abs("../..")

// build compiles the multicall binary and the named standalone binaries.
func build(t *testing.T, standalone ...string) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	hades := filepath.Join(dir, "hades")
	if out, err := exec.Command("go", "build", "-o", hades, "../../cmd/hades").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	bins := map[string]string{}
	for _, name := range standalone {
		bins[name] = filepath.Join(dir, "standalone-"+name)
		if out, err := exec.Command("go", "build", "-o", bins[name], "../../cmd/"+name).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	return hades, bins
}

type result struct {
	code           int
	stdout, stderr string
}

func invoke(t *testing.T, argv0 string, path string, args ...string) result {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Args[0] = argv0
	cmd.Dir = root
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{code, out.String(), errb.String()}
}

// TestDispatchMatchesStandaloneBinaries runs the same arguments through the
// standalone binary, `hades <role>` and a hades-<role> symlink, and requires
// identical exit codes and output (usage text and errors included).
func TestDispatchMatchesStandaloneBinaries(t *testing.T) {
	cases := []struct {
		role string
		args []string
	}{
		{"hades-check", nil}, // contract suite: exit 0
		{"hades-check", []string{"-kind", "telemetry", "-file", "/nonexistent.json"}}, // exit 1
		{"hades-check", []string{"-h"}},                                               // usage: exit 2
		{"hades-check", []string{"-no-such-flag"}},                                    // flag error: exit 2
		{"hades-pki", []string{"-h"}},
		{"hades-site", []string{"-action", "bogus", "-manifest", "/nonexistent"}},
		{"hades-service", []string{"-action", "status", "-config", "/nonexistent"}},
		{"hades-normalizer", []string{"-h"}},
		{"hades-history", []string{"-h"}},
		{"hades-transfer", []string{"-h"}},
		{"hades-gateway", []string{"-h"}},
		{"hades-actuator", []string{"-h"}},
		{"hades-ctl", []string{"status"}}, // usage error: exit 1
	}
	names := map[string]bool{}
	for _, c := range cases {
		names[c.role] = true
	}
	var list []string
	for n := range names {
		list = append(list, n)
	}
	hades, bins := build(t, list...)
	link := filepath.Join(t.TempDir(), "links")
	os.MkdirAll(link, 0o755)
	for _, c := range cases {
		t.Run(c.role+" "+strings.Join(c.args, " "), func(t *testing.T) {
			want := invoke(t, c.role, bins[c.role], c.args...)
			viaSub := invoke(t, "hades", hades, append([]string{strings.TrimPrefix(c.role, "hades-")}, c.args...)...)
			symlink := filepath.Join(link, c.role)
			os.Remove(symlink)
			if err := os.Symlink(hades, symlink); err != nil {
				t.Fatal(err)
			}
			viaLink := invoke(t, c.role, symlink, c.args...)
			for name, got := range map[string]result{"hades <role>": viaSub, "symlink": viaLink} {
				if got.code != want.code || normalize(got.stderr) != normalize(want.stderr) || got.stdout != want.stdout {
					t.Errorf("%s differs from standalone %s:\nwant code %d stderr %q\ngot  code %d stderr %q", name, c.role, want.code, want.stderr, got.code, got.stderr)
				}
			}
		})
	}
}

// normalize drops log timestamps (log.Fatal-style commands prefix them).
func normalize(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if len(l) > 20 && l[4] == '/' && l[7] == '/' && l[10] == ' ' {
			l = l[20:]
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n")
}

func TestEveryRoleIsDispatchable(t *testing.T) {
	entries, _ := os.ReadDir("../../cmd")
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == "hades" || name == "hades-opcua-mock" {
			continue // the multicall itself; a build-tagged test fixture
		}
		if _, ok := Roles[name]; !ok {
			t.Errorf("cmd/%s is not reachable through hades", name)
		}
	}
	var out, errb bytes.Buffer
	if code := Run([]string{"hades", "no-such"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "unknown command") {
		t.Fatal("an unknown command must print usage and exit 2:", code, errb.String())
	}
	if code := Run([]string{"hades"}, &out, &errb); code != 2 {
		t.Fatal("no command must exit 2")
	}
}

func TestInitScaffoldsAValidSite(t *testing.T) {
	hades, _ := build(t)
	for _, profile := range []string{"demo", "secure-site"} {
		t.Run(profile, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "site")
			r := invoke(t, "hades", hades, "init", "-profile", profile, "-out-dir", dir, "-root", root)
			if r.code != 0 || !strings.Contains(r.stdout, "next: hades up --site-dir "+dir) {
				t.Fatalf("init: %+v", r)
			}
			for _, d := range []string{"site.json", "catalog/metadata.json", "wal", "quarantine", "services/history.json"} {
				if _, err := os.Stat(filepath.Join(dir, d)); err != nil {
					t.Fatal("missing", d)
				}
			}
			if r := invoke(t, "hades", hades, "check", "-site-dir", dir); r.code != 0 {
				t.Fatalf("a fresh site must pass hades check: %+v", r)
			}
			var site SiteFile
			raw, _ := os.ReadFile(filepath.Join(dir, "site.json"))
			json.Unmarshal(raw, &site)
			if profile == "secure-site" {
				if !site.Lab || !strings.Contains(r.stdout, "lab credentials") {
					t.Fatal("secure-site credentials must be marked as lab identities")
				}
				if _, err := os.Stat(filepath.Join(site.PKI, "history-adapter.key")); err != nil {
					t.Fatal("scoped identities must be provisioned:", err)
				}
			}
			var svc map[string]any
			raw, _ = os.ReadFile(filepath.Join(dir, "services/history.json"))
			json.Unmarshal(raw, &svc)
			if svc["binary"] != site.Binary || svc["args"].([]any)[0] != "history" {
				t.Fatal("services must run the digest-pinned multicall with their role:", svc)
			}
			// Tampering is caught: a changed catalog or a swapped binary fails check.
			if r := invoke(t, "hades", hades, "init", "-profile", profile, "-out-dir", dir, "-root", root); r.code == 0 {
				t.Fatal("init must refuse to overwrite a site")
			}
		})
	}
}

func TestCheckDetectsTampering(t *testing.T) {
	hades, _ := build(t)
	fresh := func() (string, SiteFile) {
		dir := filepath.Join(t.TempDir(), "site")
		if r := invoke(t, "hades", hades, "init", "-profile", "demo", "-out-dir", dir, "-root", root); r.code != 0 {
			t.Fatal(r)
		}
		var site SiteFile
		raw, _ := os.ReadFile(filepath.Join(dir, "site.json"))
		json.Unmarshal(raw, &site)
		return dir, site
	}
	dir, site := fresh()
	os.WriteFile(filepath.Join(dir, "catalog/metadata.json"), []byte(`{"asset_id":"x"}`), 0o600)
	if r := invoke(t, "hades", hades, "check", "-site-dir", dir); r.code == 0 {
		t.Fatal("an invalid catalog must fail check")
	}
	dir, site = fresh()
	// Run check from an untouched copy: rewriting the binary that is executing
	// would be killed on macOS (code signature) before it could report.
	checker := filepath.Join(t.TempDir(), "hades-checker")
	raw, _ := os.ReadFile(site.Binary)
	os.WriteFile(checker, raw, 0o755)
	os.WriteFile(site.Binary, append(raw, 0), 0o755) // the pinned binary changes
	if r := invoke(t, "hades", checker, "check", "-site-dir", dir); r.code == 0 || !strings.Contains(r.stderr, "digest mismatch") {
		t.Fatalf("a changed binary must fail check: %+v", r)
	}
}

func TestInitIsAtomicOnFailure(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "site")
	var out, errb bytes.Buffer
	// A root without contracts fails after nothing is created.
	if code := Init([]string{"-profile", "demo", "-out-dir", dir, "-root", t.TempDir()}, &out, &errb); code == 0 {
		t.Fatal("init with an invalid root must fail")
	}
	// A failing step mid-scaffold (unknown profile is checked first; force a
	// failure after staging by making the catalog source unreadable).
	fakeRoot := t.TempDir()
	os.MkdirAll(filepath.Join(fakeRoot, "schemas/v1"), 0o755)
	os.WriteFile(filepath.Join(fakeRoot, "schemas/v1/telemetry.schema.json"), []byte("{}"), 0o644)
	if code := Init([]string{"-profile", "demo", "-out-dir", dir, "-root", fakeRoot}, &out, &errb); code == 0 {
		t.Fatal("init without a catalog must fail")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Fatalf("a failed init must leave nothing behind, found %v", entries)
	}
	if code := Init([]string{"-profile", "demo", "-out-dir", dir, "-root", root}, &out, &errb); code != 0 {
		t.Fatal("init must succeed after a failed attempt:", errb.String())
	}
}

func TestComposeProjectIsValidAndPerSite(t *testing.T) {
	a, b := composeProject("/x/My.Site"), composeProject("/y/My.Site")
	for _, p := range []string{a, b} {
		for _, r := range p {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				t.Fatalf("invalid compose project name %q", p)
			}
		}
	}
	if a == b {
		t.Fatal("two sites with the same directory name must not share a compose project")
	}
}

func TestSiteDirFlagSpellings(t *testing.T) {
	for _, args := range [][]string{{"-site-dir", "d"}, {"--site-dir", "d"}, {"-site-dir=d"}, {"--site-dir=d"}, {"-root", ".", "-site-dir", "d"}} {
		if dir, ok := siteDirArg(args); !ok || dir != "d" {
			t.Errorf("%v: got %q %v", args, dir, ok)
		}
	}
	if _, ok := siteDirArg([]string{"-kind", "telemetry", "-file", "x.json"}); ok {
		t.Fatal("plain hades-check flags must not be taken for -site-dir")
	}
}
