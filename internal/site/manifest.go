// Package site implements reviewed single-site reference releases, not fleet reconciliation.
package site

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/transport"
)

const MaxFile = 64 * 1024 * 1024

var name = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var hash = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Manifest struct {
	Format    string            `json:"format"`
	Revision  string            `json:"revision"`
	Project   string            `json:"project"`
	Profile   string            `json:"profile"`
	Metadata  string            `json:"metadata"`
	Files     map[string]string `json:"files"`
	Secrets   map[string]string `json:"secret_references,omitempty"`
	Ports     map[string]string `json:"ports,omitempty"`
	HostState map[string]string `json:"host_state,omitempty"`
}

func Decode(raw []byte) (Manifest, error) {
	var m Manifest
	if _, err := contracts.Decode(raw); err != nil {
		return m, err
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	err := d.Decode(&m)
	return m, err
}
func Digest(m Manifest) string {
	raw, _ := json.Marshal(m)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func fileHash(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }
func Required(profile, metadata string) []string {
	paths := []string{"config/normalization/temperature.blobl", "config/gateway/pilot.example.json", metadata, "schemas/v1/common.schema.json", "schemas/v1/telemetry.schema.json", "schemas/v1/metadata.schema.json", "schemas/v1/status.schema.json", "deploy/broker/Dockerfile", "deploy/historian/Dockerfile"}
	if profile == "secure" {
		return append(paths, "deploy/compose.secure.yaml", "config/broker/security/mosquitto.conf", "config/broker/security/roles.json", "deploy/historian/Proxy.Dockerfile", "artifacts/hades-history-proxy")
	}
	return append(paths, "deploy/compose.yaml", "config/broker/mosquitto.conf", "config/broker/dev.acl")
}
func safe(path string) bool {
	return path != "." && !filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.HasPrefix(path, "../") && !strings.Contains(path, "\\")
}

// Reject symlink components rather than following config or secret substitutions.
func Read(root, path string, limit int64) ([]byte, error) {
	if !safe(path) {
		return nil, fmt.Errorf("unsafe relative file %q", path)
	}
	current := root
	for _, part := range strings.Split(path, "/") {
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink rejected: %s", path)
		}
	}
	st, err := os.Stat(current)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, fmt.Errorf("invalid file type/size: %s", path)
	}
	f, err := os.Open(current)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(raw) > int(limit) {
		return nil, fmt.Errorf("file grew beyond limit")
	}
	return raw, err
}
func Prepare(root, revision, project, profile, metadata string, secrets, ports map[string]string) (Manifest, error) {
	m := Manifest{Format: "openump-site/1", Revision: revision, Project: project, Profile: profile, Metadata: metadata, Files: map[string]string{}, Secrets: secrets, Ports: ports}
	for _, path := range Required(profile, metadata) {
		raw, err := Read(root, path, MaxFile)
		if err != nil {
			return m, err
		}
		m.Files[path] = fileHash(raw)
	}
	return m, Validate(root, m)
}
func Validate(root string, m Manifest) error {
	if m.Format != "openump-site/1" || !name.MatchString(m.Revision) || !name.MatchString(m.Project) || (m.Profile != "dev" && m.Profile != "secure") {
		return fmt.Errorf("format/revision/project/profile invalid")
	}
	if !safe(m.Metadata) || !strings.HasPrefix(m.Metadata, "examples/site/") || !strings.HasSuffix(m.Metadata, ".json") {
		return fmt.Errorf("metadata must be a reviewed examples/site JSON snapshot")
	}
	required := Required(m.Profile, m.Metadata)
	if len(m.Files) != len(required) {
		return fmt.Errorf("release file list must match selected profile")
	}
	for _, path := range required {
		raw, err := Read(root, path, MaxFile)
		if err != nil {
			return fmt.Errorf("artifact %s: %w", path, err)
		}
		if !hash.MatchString(m.Files[path]) || fileHash(raw) != m.Files[path] {
			return fmt.Errorf("artifact digest mismatch: %s", path)
		}
	}
	if _, err := sample.LoadContext(root, filepath.Join(root, m.Metadata)); err != nil {
		return fmt.Errorf("schema/asset context: %w", err)
	}
	allowedPorts := map[string]bool{"HADES_MQTT_PORT": true, "HADES_VM_PORT": true}
	if m.Profile == "secure" {
		allowedPorts = map[string]bool{"HADES_SECURE_MQTT_PORT": true, "HADES_SECURE_HISTORY_PORT": true}
	}
	for key, value := range m.Ports {
		port, err := strconv.Atoi(value)
		if !allowedPorts[key] || err != nil || port < 1024 || port > 65535 {
			return fmt.Errorf("invalid published port reference: %s", key)
		}
	}
	for key, path := range m.Secrets {
		if key != "HADES_SECURITY_DIR" || m.Profile != "secure" || !filepath.IsAbs(path) {
			return fmt.Errorf("invalid secret reference: %s", key)
		}
	}
	if m.Profile == "secure" {
		dir := m.Secrets["HADES_SECURITY_DIR"]
		if dir == "" {
			return fmt.Errorf("HADES_SECURITY_DIR secret reference required")
		}
		st, err := os.Lstat(dir)
		if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("credential directory must be private mode 0700")
		}
		for _, role := range []string{"broker", "history-proxy"} {
			for _, file := range []string{"ca.crt", role + ".crt", role + ".key"} {
				if _, err := Read(dir, file, 1024*1024); err != nil {
					return fmt.Errorf("credential %s unavailable", file)
				}
			}
			key, _ := os.Stat(filepath.Join(dir, role+".key"))
			if key.Mode().Perm()&0077 != 0 {
				return fmt.Errorf("private key permissions unsafe: %s", role)
			}
			credentials, err := transport.Server(transport.Paths{CA: dir + "/ca.crt", Cert: dir + "/" + role + ".crt", Key: dir + "/" + role + ".key"})
			if err != nil {
				return fmt.Errorf("invalid TLS material: %s", role)
			}
			leaf, err := x509.ParseCertificate(credentials.Certificates[0].Certificate[0])
			if err != nil {
				return fmt.Errorf("invalid server certificate: %s", role)
			}
			intermediates := x509.NewCertPool()
			for _, der := range credentials.Certificates[0].Certificate[1:] {
				certificate, err := x509.ParseCertificate(der)
				if err != nil {
					return err
				}
				intermediates.AddCert(certificate)
			}
			if _, err = leaf.Verify(x509.VerifyOptions{Roots: credentials.RootCAs, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
				return fmt.Errorf("untrusted/expired server certificate: %s", role)
			}

		}
	}
	allowedState := map[string]bool{"history-intake": true, "history-quarantine": true, "transfer-intake": true, "transfer-quarantine": true, "normalization-quarantine": true}
	for key, path := range m.HostState {
		if !allowedState[key] || !filepath.IsAbs(path) || path == "/" {
			return fmt.Errorf("invalid host state reference: %s", key)
		}
	}
	return nil
}
func write(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func Stage(root, destination string, m Manifest) error {
	if err := Validate(root, m); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(destination)
		}
	}()
	for path, digest := range m.Files {
		raw, err := Read(root, path, MaxFile)
		if err != nil {
			return err
		}
		if fileHash(raw) != digest {
			return fmt.Errorf("artifact changed during stage: %s", path)
		}
		if err = write(filepath.Join(destination, path), raw); err != nil {
			return err
		}
		if err = os.Chmod(filepath.Join(destination, path), 0644); err != nil {
			return err
		}
		if path == "artifacts/hades-history-proxy" {
			if err = os.Chmod(filepath.Join(destination, path), 0755); err != nil {
				return err
			}
		}
	}
	raw, _ := json.MarshalIndent(m, "", "  ")
	if err := write(filepath.Join(destination, "site.json"), raw); err != nil {
		return err
	}
	raw = Labels(m)
	if err := write(filepath.Join(destination, "labels.json"), raw); err != nil {
		return err
	}
	if err := filepath.WalkDir(destination, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return syncDir(path)
		}
		return nil
	}); err != nil {
		return err
	}
	complete = true
	return syncDir(filepath.Dir(destination))
}

func Labels(m Manifest) []byte {
	labels := map[string]any{"services": map[string]any{}}
	services := []string{"broker", "historian"}
	if m.Profile == "secure" {
		services = append(services, "history-proxy", "security-init")
	}
	for _, service := range services {
		labels["services"].(map[string]any)[service] = map[string]any{"labels": map[string]string{"org.openump.config-revision": m.Revision, "org.openump.config-digest": Digest(m)}}
	}
	raw, _ := json.Marshal(labels)
	return raw
}
