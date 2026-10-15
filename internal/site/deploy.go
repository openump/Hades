package site

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openump/Hades/internal/storage"
)

type Runner func(context.Context, string, []string, []string) ([]byte, error)

// Bound subprocess output; no shell or inherited Compose/Hades substitutions.
func Docker(ctx context.Context, dir string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	cmd.Env = env
	var output limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker operation failed: %w: %s", err, output.String())
	}
	return output.Bytes(), nil
}

type limitedOutput struct{ bytes.Buffer }

func (b *limitedOutput) Write(raw []byte) (int, error) {
	n := len(raw)
	if b.Len() < 1024*1024 {
		b.Buffer.Write(raw[:min(n, 1024*1024-b.Len())])
	}
	return n, nil
}

type Manager struct {
	Dir   string
	run   Runner
	store *storage.Store
}

func Open(dir string, run Runner) (*Manager, error) {
	if run == nil {
		run = Docker
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("manager state must be private mode 0700 without symlink")
	}
	s, err := storage.Open(filepath.Join(abs, "control"), 4, 1024*1024, 128*1024*1024)
	if err != nil {
		return nil, err
	}
	return &Manager{Dir: abs, run: run, store: s}, nil
}
func (m *Manager) Close() error { return m.store.Close() }
func (m *Manager) Active() (Manifest, error) {
	raw, ok := m.store.Records["active"]
	if !ok {
		return Manifest{}, fmt.Errorf("no committed active revision")
	}
	return Decode(raw)
}
func environment(manifest Manifest) []string {
	result := []string{}
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "COMPOSE_") && !strings.HasPrefix(item, "HADES_") {
			result = append(result, item)
		}
	}
	for key, value := range manifest.Ports {
		result = append(result, key+"="+value)
	}
	for key, value := range manifest.Secrets {
		result = append(result, key+"="+value)
	}
	return append(result, "COMPOSE_DISABLE_ENV_FILE=1")
}
func compose(manifest Manifest, release string, args ...string) []string {
	file := "compose.yaml"
	if manifest.Profile == "secure" {
		file = "compose.secure.yaml"
	}
	base := []string{"compose", "--env-file", "/dev/null", "-p", manifest.Project, "--profile", "history", "-f", filepath.Join(release, "deploy", file), "-f", filepath.Join(release, "labels.json")}
	return append(base, args...)
}
func (m *Manager) Release(manifest Manifest) string {
	return filepath.Join(m.Dir, "releases", Digest(manifest))
}
func (m *Manager) ValidateCompose(ctx context.Context, manifest Manifest, release string) error {
	raw, err := Read(release, "labels.json", 8192)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, Labels(manifest)) {
		return fmt.Errorf("generated revision labels modified")
	}
	_, err = m.run(ctx, release, compose(manifest, release, "config", "--quiet"), environment(manifest))
	return err
}
func (m *Manager) Apply(ctx context.Context, root string, manifest Manifest) error {
	if err := Validate(root, manifest); err != nil {
		return err
	}
	if _, exists := m.store.Records["active"]; exists {
		if _, err := m.Active(); err != nil {
			return err
		}
	}
	if old, err := m.Active(); err == nil {
		// Keep persistence format, project/volume identity, topic mapping and metadata
		// context stable. A migration requires its own explicit reconciliation plan.
		if old.Project != manifest.Project || old.Profile != manifest.Profile || old.Files["config/broker/security/roles.json"] != manifest.Files["config/broker/security/roles.json"] || old.Metadata != manifest.Metadata || old.Files[old.Metadata] != manifest.Files[manifest.Metadata] {
			return fmt.Errorf("incompatible deployment identity/profile/metadata change; migration required")
		}
		if old.Revision == manifest.Revision && Digest(old) != Digest(manifest) {
			return fmt.Errorf("revision already names different content")
		}
	}
	if err := os.MkdirAll(filepath.Join(m.Dir, "releases"), 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(m.Dir, "releases"))
	if err != nil {
		return err
	}
	if len(entries) >= 32 {
		if _, err = os.Stat(m.Release(manifest)); os.IsNotExist(err) {
			return fmt.Errorf("32 release limit reached; reconcile retention before adding revisions")
		}
	}
	for _, entry := range entries {
		raw, err := Read(filepath.Join(m.Dir, "releases", entry.Name()), "site.json", 65536)
		if err != nil {
			return err
		}
		old, err := Decode(raw)
		if err != nil {
			return err
		}
		if old.Revision == manifest.Revision && Digest(old) != Digest(manifest) {
			return fmt.Errorf("revision already names different content")
		}
	}
	release := m.Release(manifest)
	if _, err := os.Stat(release); os.IsNotExist(err) {
		if err = Stage(root, release, manifest); err != nil {
			return err
		}
	}
	if err := Validate(release, manifest); err != nil {
		return err
	}
	if err := m.ValidateCompose(ctx, manifest, release); err != nil {
		return err
	}
	if _, err := m.run(ctx, release, compose(manifest, release, "up", "--build", "-d", "--wait", "--wait-timeout", "90"), environment(manifest)); err != nil {
		return fmt.Errorf("apply incomplete; inspect live status and roll back committed revision: %w", err)
	}
	if err := m.Live(ctx, manifest); err != nil {
		return err
	}
	raw, _ := json.Marshal(manifest)
	if err := m.store.Put("active", raw); err != nil {
		return fmt.Errorf("live applied but active commit failed: %w", err)
	}
	return nil
}
func (m *Manager) Live(ctx context.Context, manifest Manifest) error {
	if err := Validate(m.Release(manifest), manifest); err != nil {
		return err
	}
	services := []string{"broker", "historian"}
	if manifest.Profile == "secure" {
		services = append(services, "history-proxy")
	}
	for _, service := range services {
		raw, err := m.run(ctx, m.Release(manifest), compose(manifest, m.Release(manifest), "ps", "-q", service), environment(manifest))
		if err != nil {
			return err
		}
		id := strings.TrimSpace(string(raw))
		if id == "" || strings.ContainsAny(id, " \r\n\t") {
			return fmt.Errorf("service %s has no unique running container", service)
		}
		raw, err = m.run(ctx, m.Dir, []string{"inspect", id}, environment(manifest))
		if err != nil {
			return err
		}
		var entries []struct {
			State  struct{ Running bool }
			Config struct{ Labels map[string]string }
		}
		if err = json.Unmarshal(raw, &entries); err != nil || len(entries) != 1 || !entries[0].State.Running || entries[0].Config.Labels["org.openump.config-revision"] != manifest.Revision || entries[0].Config.Labels["org.openump.config-digest"] != Digest(manifest) {
			return fmt.Errorf("service %s live revision mismatch or not running", service)
		}
	}
	return nil
}
func (m *Manager) Rollback(ctx context.Context, digest string) error {
	if !hash.MatchString(digest) {
		return fmt.Errorf("invalid release digest")
	}
	root := filepath.Join(m.Dir, "releases", digest)
	raw, err := Read(root, "site.json", 65536)
	if err != nil {
		return err
	}
	manifest, err := Decode(raw)
	if err != nil || Digest(manifest) != digest {
		return fmt.Errorf("release manifest digest mismatch")
	}
	return m.Apply(ctx, root, manifest)
}
func Deadline() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Minute)
}

func (m *Manager) Check(ctx context.Context, root string, manifest Manifest) error {
	temporary, err := os.MkdirTemp(m.Dir, "validate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	release := filepath.Join(temporary, "release")
	if err = Stage(root, release, manifest); err != nil {
		return err
	}
	return m.ValidateCompose(ctx, manifest, release)
}
