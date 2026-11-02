package cli

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openump/Hades/internal/supervisor"
)

// Profiles hades init can scaffold.
var profiles = map[string]string{
	"demo":        "anonymous loopback development broker and historian; simulator -> MQTT -> history and a consumer",
	"secure-site": "hardened mTLS reference profile with scoped lab identities (not production credentials)",
}

// SiteFile is the scaffolded site description hades up reads.
type SiteFile struct {
	Format   string    `json:"format"` // "hades-site-dir/1"
	Profile  string    `json:"profile"`
	Root     string    `json:"root"`     // contract root (schemas, examples, config)
	Metadata string    `json:"metadata"` // approved metadata snapshot, relative to Root
	Binary   string    `json:"binary"`   // absolute hades multicall path, digest-pinned in services/
	Created  time.Time `json:"created_at"`
	Services []string  `json:"services"` // names under services/<name>.json
	PKI      string    `json:"pki,omitempty"`
	MQTTPort int       `json:"mqtt_port"`
	VMPort   int       `json:"historian_port"`
	Project  string    `json:"compose_project"`
	Lab      bool      `json:"lab_credentials"`
}

// Init scaffolds a site directory: site.json, catalog/, wal/, quarantine/,
// one digest-pinned supervisor definition per service and, for secure-site,
// scoped lab mTLS identities.
func Init(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hades init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	profile := fs.String("profile", "demo", "demo or secure-site")
	out := fs.String("out-dir", "", "new site directory (must not exist or be empty)")
	root := fs.String("root", ".", "Hades contract root (schemas, examples, config)")
	mqttPort := fs.Int("mqtt-port", 1883, "loopback broker port (demo)")
	vmPort := fs.Int("historian-port", 8428, "loopback historian port (demo)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := initSite(*profile, *out, *root, *mqttPort, *vmPort); err != nil {
		fmt.Fprintln(stderr, "hades init:", err)
		return 1
	}
	fmt.Fprintf(stdout, "site scaffolded in %s (profile %s)\n", *out, *profile)
	if *profile != "demo" {
		fmt.Fprintln(stdout, "lab credentials: scoped evaluation identities only; replace with your site CA before production")
	}
	fmt.Fprintf(stdout, "next: hades up --site-dir %s\n", *out)
	return 0
}

// initSite scaffolds into a sibling temporary directory and renames it into
// place, so a failed init never leaves a half-built site behind.
func initSite(profile, out, root string, mqttPort, vmPort int) error {
	if out == "" {
		return fmt.Errorf("-out-dir is required")
	}
	final, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(final); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s is not empty; refusing to overwrite a site", out)
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(final), "."+filepath.Base(final)+".init-")
	if err != nil {
		return err
	}
	if err := scaffold(profile, staging, final, root, mqttPort, vmPort); err != nil {
		os.RemoveAll(staging)
		return err
	}
	os.Remove(final) // an existing empty directory
	if err := os.Rename(staging, final); err != nil {
		os.RemoveAll(staging)
		return err
	}
	return nil
}

// scaffold writes a site into dir whose final location is final; every path
// recorded in the site points at final.
func scaffold(profile, dir, final, root string, mqttPort, vmPort int) error {
	for _, p := range []int{mqttPort, vmPort} {
		if p < 1024 || p > 65535 {
			return fmt.Errorf("ports must be 1024..65535")
		}
	}
	if _, ok := profiles[profile]; !ok {
		return fmt.Errorf("unknown profile %q (demo or secure-site)", profile)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(absRoot, "schemas/v1/telemetry.schema.json")); err != nil {
		return fmt.Errorf("-root %s is not a Hades contract root", root)
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if binary, err = filepath.EvalSymlinks(binary); err != nil {
		return err
	}
	for _, d := range []string{"catalog", "wal", "quarantine", "services", "state", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			return err
		}
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	metadata := "examples/site/metadata.json"
	raw, err := os.ReadFile(filepath.Join(absRoot, metadata))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog", "metadata.json"), raw, 0o600); err != nil {
		return err
	}
	site := SiteFile{Format: "hades-site-dir/1", Profile: profile, Root: absRoot, Metadata: metadata, Binary: binary, Created: time.Now().UTC().Truncate(time.Second),
		MQTTPort: mqttPort, VMPort: vmPort, Project: composeProject(final)}
	if profile == "secure-site" {
		site.PKI, site.Lab = filepath.Join(final, "pki"), true
		if code := Roles["hades-pki"]([]string{"-out", filepath.Join(dir, "pki")}); code != 0 {
			return fmt.Errorf("provisioning lab identities failed")
		}
	}
	defs, err := serviceDefinitions(site, final)
	if err != nil {
		return err
	}
	digest, err := supervisor.HashFile(binary)
	if err != nil {
		return err
	}
	for _, d := range defs {
		d.cfg.Binary, d.cfg.BinarySHA256 = binary, digest
		// Validate against the staged paths; the definitions record final ones.
		staged := d.cfg
		staged.State, staged.Directory = filepath.Join(dir, "state", d.name), site.Root
		if err := supervisor.Validate(staged); err != nil {
			return fmt.Errorf("service %s: %w", d.name, err)
		}
		encoded, _ := json.MarshalIndent(d.cfg, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "services", d.name+".json"), append(encoded, '\n'), 0o600); err != nil {
			return err
		}
		site.Services = append(site.Services, d.name)
	}
	encoded, _ := json.MarshalIndent(site, "", "  ")
	return os.WriteFile(filepath.Join(dir, "site.json"), append(encoded, '\n'), 0o600)
}

type serviceDef struct {
	name string
	cfg  supervisor.Config
}

// serviceDefinitions returns the long-running services of a profile. Each
// runs the multicall binary with its role as the first argument, so one
// pinned digest covers every role.
func serviceDefinitions(site SiteFile, dir string) ([]serviceDef, error) {
	metadata := filepath.Join(dir, "catalog", "metadata.json")
	state := func(name string) string { return filepath.Join(dir, "state", name) }
	def := func(name, ready string, args ...string) serviceDef {
		return serviceDef{name, supervisor.Config{Revision: "init-" + name, Args: args, Directory: site.Root, State: state(name), Ready: ready, LogBytes: 1 << 20,
			WritablePaths: []string{filepath.Join(dir, "wal"), filepath.Join(dir, "quarantine")}}}
	}
	switch site.Profile {
	case "demo":
		broker := fmt.Sprintf("mqtt://127.0.0.1:%d", site.MQTTPort)
		return []serviceDef{
			def("history", `"type":"history_ready"`, "history", "-broker", broker, "-historian", fmt.Sprintf("http://127.0.0.1:%d", site.VMPort), "-root", site.Root, "-config", metadata,
				"-intake", filepath.Join(dir, "wal", "history"), "-quarantine", filepath.Join(dir, "quarantine", "history")),
		}, nil
	case "secure-site":
		pki := site.PKI
		tlsArgs := func(role string) []string {
			return []string{"-tls-ca", filepath.Join(pki, "ca.crt"), "-tls-cert", filepath.Join(pki, role+".crt"), "-tls-key", filepath.Join(pki, role+".key")}
		}
		broker := "tls://127.0.0.1:8883"
		history := append([]string{"history", "-broker", broker, "-historian", "https://127.0.0.1:8430", "-root", site.Root, "-config", metadata,
			"-intake", filepath.Join(dir, "wal", "history"), "-quarantine", filepath.Join(dir, "quarantine", "history")}, tlsArgs("history-adapter")...)
		return []serviceDef{def("history", `"type":"history_ready"`, history...)}, nil
	}
	return nil, fmt.Errorf("unknown profile")
}

// composeProject derives a valid, per-site Compose project name: lowercase
// [a-z0-9_-] from the directory name plus a short hash of its absolute path,
// so two sites never share a project or its volumes.
func composeProject(dir string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(dir)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if len(name) > 32 {
		name = name[:32]
	}
	sum := sha256.Sum256([]byte(dir))
	return fmt.Sprintf("hades-%s-%x", name, sum[:4])
}
