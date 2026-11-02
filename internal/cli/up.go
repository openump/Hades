package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/openump/Hades/internal/supervisor"
)

// LoadSite reads and checks a scaffolded site directory.
func LoadSite(dir string) (SiteFile, error) {
	var site SiteFile
	raw, err := os.ReadFile(filepath.Join(dir, "site.json"))
	if err != nil {
		return site, err
	}
	if err := json.Unmarshal(raw, &site); err != nil {
		return site, err
	}
	if site.Format != "hades-site-dir/1" {
		return site, fmt.Errorf("%s: not a hades init site directory", dir)
	}
	return site, nil
}

// Up starts a scaffolded site in the foreground: dependencies first, then
// every service under its own digest-bound supervisor, then (demo) a
// continuous simulator. SIGINT or SIGTERM stops everything.
func Up(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hades up", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("site-dir", "", "site directory from hades init")
	demo := fs.Bool("demo", false, "shorthand: hades init --profile demo into ./hades-demo if missing, then up")
	deps := fs.Bool("deps", true, "start the broker/historian with Docker Compose (false: use already-running local endpoints)")
	interval := fs.Duration("interval", 2*time.Second, "demo simulator publish interval")
	keep := fs.Bool("keep-deps", false, "leave the broker/historian running after the site stops")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *demo && *dir == "" {
		*dir = "hades-demo"
		if _, err := os.Stat(filepath.Join(*dir, "site.json")); err != nil {
			if code := Init([]string{"-profile", "demo", "-out-dir", *dir}, stdout, stderr); code != 0 {
				return code
			}
		}
	}
	if *dir == "" {
		fmt.Fprintln(stderr, "hades up: -site-dir (or --demo) is required")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := up(ctx, *dir, *deps, *keep, *interval, stdout); err != nil {
		fmt.Fprintln(stderr, "hades up:", err)
		return 1
	}
	return 0
}

func emit(w io.Writer, mu *sync.Mutex, event map[string]any) {
	event["at"] = time.Now().UTC().Format(time.RFC3339)
	raw, _ := json.Marshal(event)
	mu.Lock()
	fmt.Fprintln(w, string(raw))
	mu.Unlock()
}

func up(ctx context.Context, dir string, deps, keepDeps bool, interval time.Duration, stdout io.Writer) error {
	site, err := LoadSite(dir)
	if err != nil {
		return err
	}
	dir, _ = filepath.Abs(dir)
	var mu sync.Mutex
	// Re-verify every pinned definition before starting anything.
	digests := map[string]string{}
	configs := map[string]supervisor.Config{}
	for _, name := range site.Services {
		cfg, err := supervisor.Load(filepath.Join(dir, "services", name+".json"))
		if err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
		if err := supervisor.Validate(cfg); err != nil {
			return fmt.Errorf("service %s: %w (rerun hades init after upgrading the binary)", name, err)
		}
		digests[name], configs[name] = supervisor.Digest(cfg), cfg
	}
	if deps {
		err := startDependencies(ctx, site, stdout, &mu)
		if !keepDeps {
			defer stopDependencies(site, stdout, &mu)
		}
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	failed := make(chan error, len(site.Services)+1)
	start := func(name string, args ...string) {
		cmd := exec.CommandContext(ctx, site.Binary, args...)
		cmd.Dir = site.Root
		logPath := filepath.Join(dir, "logs", name+".log")
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			failed <- err
			return
		}
		cmd.Stdout, cmd.Stderr = logFile, logFile
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		cmd.WaitDelay = 15 * time.Second
		if err := cmd.Start(); err != nil {
			logFile.Close()
			failed <- fmt.Errorf("%s: %w", name, err)
			return
		}
		emit(stdout, &mu, map[string]any{"type": "service_started", "service": name, "pid": cmd.Process.Pid, "log": logPath})
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer logFile.Close()
			err := cmd.Wait()
			if ctx.Err() == nil {
				failed <- fmt.Errorf("%s exited: %v (see %s)", name, err, logPath)
			}
		}()
	}
	for _, name := range site.Services {
		start(name, "service", "-action", "run", "-config", filepath.Join(dir, "services", name+".json"), "-digest", digests[name])
	}
	// Wait for every supervised service to report ready.
	ready := map[string]bool{}
	for deadline := time.Now().Add(60 * time.Second); len(ready) < len(site.Services); time.Sleep(500 * time.Millisecond) {
		select {
		case err := <-failed:
			cancel()
			wg.Wait()
			return err
		case <-ctx.Done():
			wg.Wait()
			return nil
		default:
		}
		for _, name := range site.Services {
			if ready[name] {
				continue
			}
			if st, err := supervisor.Probe(ctx, configs[name]); err == nil && st.Ready {
				ready[name] = true
				emit(stdout, &mu, map[string]any{"type": "service_healthy", "service": name, "child_pid": st.ChildPID, "revision": st.Revision})
			}
		}
		if time.Now().After(deadline) {
			cancel()
			wg.Wait()
			return fmt.Errorf("services not healthy within 60s; see %s", filepath.Join(dir, "logs"))
		}
	}
	if site.Profile == "demo" {
		go simulate(ctx, site, dir, interval, stdout, &mu, failed)
	}
	emit(stdout, &mu, map[string]any{"type": "site_up", "profile": site.Profile, "services": site.Services, "hint": "Ctrl+C stops the site"})
	select {
	case <-ctx.Done():
	case err := <-failed:
		cancel()
		wg.Wait()
		return err
	}
	emit(stdout, &mu, map[string]any{"type": "site_stopping"})
	cancel()
	wg.Wait()
	emit(stdout, &mu, map[string]any{"type": "site_stopped"})
	return nil
}

// simulate runs one long-lived simulator that publishes one demo observation
// per interval while the site is up; it is restarted if it completes.
func simulate(ctx context.Context, site SiteFile, dir string, interval time.Duration, stdout io.Writer, mu *sync.Mutex, failed chan<- error) {
	const batch = 10000 // the simulator's maximum -count
	for round := 0; ctx.Err() == nil; round++ {
		cmd := exec.CommandContext(ctx, site.Binary, "simulator", "-broker", fmt.Sprintf("mqtt://127.0.0.1:%d", site.MQTTPort), "-root", site.Root,
			"-config", filepath.Join(dir, "catalog", "metadata.json"), "-count", fmt.Sprint(batch), "-scenario", "normal",
			"-interval", interval.String(), "-timeout", (time.Duration(batch)*interval + time.Minute).String())
		cmd.Dir = site.Root
		emit(stdout, mu, map[string]any{"type": "simulator_started", "interval": interval.String(), "round": round})
		if out, err := cmd.CombinedOutput(); err != nil && ctx.Err() == nil {
			failed <- fmt.Errorf("simulator: %v: %s", err, out)
			return
		}
	}
}

// stopDependencies stops (does not remove) the site's Compose dependencies;
// broker and historian volumes are kept.
func stopDependencies(site SiteFile, stdout io.Writer, mu *sync.Mutex) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", site.Project, "-f", filepath.Join(site.Root, "deploy/compose.yaml"), "--profile", "history", "stop")
	cmd.Dir = site.Root
	out, err := cmd.CombinedOutput()
	if err != nil {
		emit(stdout, mu, map[string]any{"type": "dependencies_stop_failed", "error": err.Error(), "output": string(out)})
		return
	}
	emit(stdout, mu, map[string]any{"type": "dependencies_stopped"})
}

// startDependencies starts the reference broker and historian with Docker
// Compose and waits until both are healthy.
func startDependencies(ctx context.Context, site SiteFile, stdout io.Writer, mu *sync.Mutex) error {
	var compose []string
	var health string
	switch site.Profile {
	case "demo":
		compose, health = []string{"compose", "-p", site.Project, "-f", filepath.Join(site.Root, "deploy/compose.yaml"), "--profile", "history", "up", "--build", "-d", "--wait"}, fmt.Sprintf("http://127.0.0.1:%d/health", site.VMPort)
	default:
		return fmt.Errorf("profile %s: start the secure broker with hades site or pass -deps=false", site.Profile)
	}
	emit(stdout, mu, map[string]any{"type": "dependencies_starting", "command": "docker " + fmt.Sprint(compose)})
	cmd := exec.CommandContext(ctx, "docker", compose...)
	cmd.Dir = site.Root
	cmd.Env = append(os.Environ(), fmt.Sprintf("HADES_MQTT_PORT=%d", site.MQTTPort), fmt.Sprintf("HADES_VM_PORT=%d", site.VMPort))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker compose: %v\n%s", err, out)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		if resp, err := client.Get(health); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("historian not healthy at %s", health)
		}
	}
	emit(stdout, mu, map[string]any{"type": "dependencies_healthy"})
	return nil
}
