// Package supervisor binds live process evidence to a reviewed binary/config.
// The service manager owns restart/resource policy; this wrapper owns graceful
// child shutdown, bounded stdout+stderr, and a private live Unix status socket.
package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/operations"
)

type Config struct {
	Finite        bool              `json:"finite,omitempty"`
	WritablePaths []string          `json:"writable_paths"`
	Revision      string            `json:"revision"`
	Binary        string            `json:"binary"`
	BinarySHA256  string            `json:"binary_sha256"`
	Args          []string          `json:"args"`
	Directory     string            `json:"directory"`
	State         string            `json:"state"`
	Ready         string            `json:"ready_pattern"`
	Files         map[string]string `json:"files"`
	LogBytes      int               `json:"log_bytes"`
}
type Status struct {
	Revision      string    `json:"revision"`
	Digest        string    `json:"digest"`
	BinarySHA256  string    `json:"binary_sha256"`
	ChildPID      int       `json:"child_pid"`
	SupervisorPID int       `json:"supervisor_pid"`
	Ready         bool      `json:"ready"`
	Started       time.Time `json:"started_at"`
	LastOutput    time.Time `json:"last_output_at"`
}

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
var checksum = regexp.MustCompile(`^[0-9a-f]{64}$`)

func Digest(cfg Config) string {
	raw, _ := json.Marshal(cfg)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func Load(path string) (Config, error) {
	var cfg Config
	info, err := os.Lstat(path)
	if err != nil {
		return cfg, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return cfg, fmt.Errorf("service definition must be private bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if _, err = contracts.Decode(raw); err != nil {
		return cfg, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&cfg)
	return cfg, err
}
func hashFile(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || !st.Mode().IsRegular() || st.Size() > 256*1024*1024 {
		return "", fmt.Errorf("artifact must be absolute bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err = io.Copy(hasher, io.LimitReader(file, 256*1024*1024+1)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}
func Validate(cfg Config) error {
	if !identifier.MatchString(cfg.Revision) || !filepath.IsAbs(cfg.State) || !filepath.IsAbs(cfg.Directory) || cfg.State == "/" || len(cfg.Ready) < 1 || len(cfg.Ready) > 128 || cfg.LogBytes < 256 || cfg.LogBytes > 5*1024*1024 || len(cfg.Args) > 128 || len(cfg.Files) > 32 {
		return fmt.Errorf("invalid bounded service configuration")
	}
	binary, err := hashFile(cfg.Binary)
	if err != nil || !checksum.MatchString(cfg.BinarySHA256) || binary != cfg.BinarySHA256 {
		return fmt.Errorf("service binary digest mismatch")
	}
	for path, want := range cfg.Files {
		got, err := hashFile(path)
		if err != nil || !checksum.MatchString(want) || got != want {
			return fmt.Errorf("referenced artifact digest mismatch: %s", path)
		}
	}
	for _, path := range cfg.WritablePaths {
		if !filepath.IsAbs(path) || path == "/" || strings.ContainsAny(path, " \t\r\n%$\\") {
			return fmt.Errorf("unsafe writable path")
		}
	}
	for _, arg := range cfg.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("invalid argument")
		}
	}
	return nil
}

type output struct {
	mu            sync.Mutex
	log           *operations.Log
	status        *Status
	pattern, tail string
}

func (o *output) Write(raw []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.log.Write(raw)
	if err != nil {
		return n, err
	}
	o.status.LastOutput = time.Now().UTC()
	combined := o.tail + string(raw)
	if strings.Contains(combined, o.pattern) {
		o.status.Ready = true
	}
	o.tail = combined[max(0, len(combined)-len(o.pattern)):]
	return n, nil
}
func Run(ctx context.Context, path, expected string) error {
	cfg, err := Load(path)
	if err != nil {
		return err
	}
	if Digest(cfg) != expected {
		return fmt.Errorf("service definition digest mismatch")
	}
	if err = Validate(cfg); err != nil {
		return err
	}
	if err = os.MkdirAll(cfg.State, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(cfg.State)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("service state must be private real directory")
	}
	logs, err := operations.OpenLog(filepath.Join(cfg.State, "events.log"), cfg.LogBytes)
	if err != nil {
		return err
	}
	defer logs.Close()
	socket := filepath.Join(cfg.State, "live.sock")
	if len(socket) > 100 {
		return fmt.Errorf("service Unix socket path exceeds portable 100-byte limit")
	}
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("unexpected status path")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0600); err != nil {
		listener.Close()
		return err
	}
	state := Status{Revision: cfg.Revision, Digest: expected, BinarySHA256: cfg.BinarySHA256, SupervisorPID: os.Getpid(), Started: time.Now().UTC()}
	out := &output{log: logs, status: &state, pattern: cfg.Ready}
	child := exec.Command(cfg.Binary, cfg.Args...)
	child.Dir = cfg.Directory
	// Avoid ambient loader/Go diagnostic variables altering the reviewed process.
	child.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + cfg.State, "LANG=C.UTF-8"}
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	child.Stdout = out
	child.Stderr = out
	child.WaitDelay = 3 * time.Second
	if err = child.Start(); err != nil {
		listener.Close()
		return err
	}
	state.ChildPID = child.Process.Pid
	finished := make(chan error, 1)
	go func() { finished <- child.Wait() }()
	server := &http.Server{ReadHeaderTimeout: time.Second, WriteTimeout: 2 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		// Compare the live launch definition/artifacts, not a stale marker on disk.
		current, err := Load(path)
		if err != nil || Digest(current) != expected {
			http.Error(w, "definition changed", 409)
			return
		}
		if err = Validate(cfg); err != nil {
			http.Error(w, "artifact changed", 409)
			return
		}
		out.mu.Lock()
		snapshot := state
		out.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snapshot)
	})}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer server.Close()
	startup := time.NewTimer(30 * time.Second)
	defer startup.Stop()
	stopChild := func() {
		syscall.Kill(-child.Process.Pid, syscall.SIGTERM)
		select {
		case <-finished:
		case <-time.After(8 * time.Second):
			syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			<-finished
		}
	}
	for {
		select {
		case err = <-finished:
			// Also terminate descendants if the main child exits before its helpers.
			syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
			if err != nil {
				return fmt.Errorf("service child exited: %w", err)
			}
			if cfg.Finite {
				return nil
			}
			return fmt.Errorf("service child exited before supervision ended")
		case <-ctx.Done():
			stopChild()
			return nil
		case err = <-served:
			stopChild()
			return err
		case <-startup.C:
			out.mu.Lock()
			ready := state.Ready
			out.mu.Unlock()
			if !ready {
				stopChild()
				return fmt.Errorf("service readiness pattern not observed within 30 seconds")
			}
		}
	}
}
func Probe(ctx context.Context, cfg Config) (Status, error) {
	var state Status
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(cfg.State, "live.sock"))
	}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost/status", nil)
	response, err := client.Do(req)
	if err != nil {
		return state, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return state, fmt.Errorf("live definition/artifact unavailable or mismatched")
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&state); err != nil {
		return state, err
	}
	if state.Digest != Digest(cfg) || state.Revision != cfg.Revision || state.BinarySHA256 != cfg.BinarySHA256 || state.ChildPID < 1 {
		return state, fmt.Errorf("live revision mismatch")
	}
	return state, nil
}

// Unit renders an installable unit. No files are installed or services started
// implicitly. Caller selects a non-root account and dependencies per deployment.
func Unit(name, user, binary, config, digest string, writable ...string) (string, error) {
	clean := func(s string) bool { return s != "" && !strings.ContainsAny(s, " \t\r\n\"'\\%$;") }
	if !identifier.MatchString(name) || !identifier.MatchString(user) || user == "root" || !filepath.IsAbs(binary) || !filepath.IsAbs(config) || !clean(binary) || !clean(config) || !checksum.MatchString(digest) {
		return "", fmt.Errorf("unsafe unit inputs")
	}
	for _, path := range writable {
		if !filepath.IsAbs(path) || path == "/" || !clean(path) {
			return "", fmt.Errorf("unsafe writable path")
		}
	}
	return fmt.Sprintf(`[Unit]
Description=Hades reviewed service %s
After=network.target
StartLimitIntervalSec=60
StartLimitBurst=5
[Service]
Type=simple
User=%s
ExecStart=%s -action run -config %s -digest %s
Restart=on-failure
RestartSec=2
CPUQuota=50%%
MemoryMax=512M
TasksMax=64
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=%s
ProtectHome=read-only
PrivateTmp=false
UMask=0077
KillMode=control-group
TimeoutStopSec=12
StandardOutput=journal
StandardError=journal
LogRateLimitIntervalSec=30s
LogRateLimitBurst=100
[Install]
WantedBy=multi-user.target
`, name, user, binary, config, digest, strings.Join(writable, " ")), nil
}
