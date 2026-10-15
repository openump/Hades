package site

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const HelperImage = "alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6"

func copyTree(root, target string) error {
	names, _, err := files(root)
	if err != nil {
		return err
	}
	for _, name := range names {
		source, err := os.Open(filepath.Join(root, name))
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(filepath.Join(target, name)), 0700); err != nil {
			source.Close()
			return err
		}
		output, err := os.OpenFile(filepath.Join(target, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			source.Close()
			return err
		}
		info, statErr := source.Stat()
		if statErr != nil {
			source.Close()
			output.Close()
			return statErr
		}
		_, copyErr := io.Copy(output, source)
		source.Close()
		modeErr := output.Chmod(info.Mode().Perm())
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if modeErr != nil {
			return modeErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// Backup stops the managed Compose stack cleanly, holds host-store locks, copies
// only cold data/configuration and restarts Compose even when backup fails.
func (m *Manager) Backup(ctx context.Context, destination string) (err error) {
	manifest, err := m.Active()
	if err != nil {
		return err
	}
	if err = Validate(m.Release(manifest), manifest); err != nil {
		return err
	}
	if err = m.Live(ctx, manifest); err != nil {
		return err
	}
	locks, err := lockHostStores(manifest)
	if err != nil {
		return err
	}
	defer func() {
		for _, lock := range locks {
			lock.Close()
		}
	}()
	if destination == "" {
		return fmt.Errorf("archive path required")
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("backup destination must not exist")
	}
	scratch, err := os.MkdirTemp(filepath.Dir(destination), ".hades-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	start := time.Now()
	// Registered before stop: partial stop errors must still attempt restart.
	defer func() {
		resume, cancel := Deadline()
		defer cancel()
		_, restartErr := m.run(resume, m.Release(manifest), compose(manifest, m.Release(manifest), "up", "--no-build", "-d", "--wait", "--wait-timeout", "90"), environment(manifest))
		if restartErr != nil {
			err = fmt.Errorf("backup result %v; stack restart failed: %w", err, restartErr)
		}
	}()
	if _, err = m.run(ctx, m.Release(manifest), compose(manifest, m.Release(manifest), "stop", "--timeout", "30"), environment(manifest)); err != nil {
		return err
	}
	var estimate int64
	ids := map[string]string{}
	for _, service := range []string{"broker", "historian"} {
		raw, e := m.run(ctx, m.Release(manifest), compose(manifest, m.Release(manifest), "ps", "-a", "-q", service), environment(manifest))
		if e != nil {
			return e
		}
		id := strings.TrimSpace(string(raw))
		if id == "" || strings.ContainsAny(id, " \r\n\t") {
			return fmt.Errorf("cold backup lacks unique %s container", service)
		}
		ids[service] = id
		raw, e = m.run(ctx, m.Dir, []string{"inspect", id}, environment(manifest))
		if e != nil {
			return e
		}
		var states []struct {
			State struct {
				Running, OOMKilled bool
				ExitCode           int
			}
		}
		if e = json.Unmarshal(raw, &states); e != nil || len(states) != 1 || states[0].State.Running || states[0].State.OOMKilled || states[0].State.ExitCode != 0 {
			return fmt.Errorf("%s did not stop cleanly; cold backup refused", service)
		}
		source, uid := "/mosquitto/data", "1883"
		if service == "historian" {
			source = "/storage"
			uid = "10001"
		}
		raw, e = m.run(ctx, m.Dir, []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--user", uid, "--volumes-from", id + ":ro", HelperImage, "du", "-sk", source}, environment(manifest))
		if e != nil {
			return e
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		fields := strings.Fields(lines[len(lines)-1])
		if len(fields) != 2 {
			return fmt.Errorf("invalid component size report")
		}
		size, e := strconv.ParseInt(fields[0], 10, 64)
		if e != nil || size < 0 || size > MaxBackup/1024 {
			return fmt.Errorf("component exceeds backup size bound")
		}
		estimate += size * 1024
	}
	for _, path := range manifest.HostState {
		_, size, e := files(path)
		if e != nil {
			return e
		}
		estimate += size
	}
	_, configSize, e := files(m.Dir)
	if e != nil {
		return e
	}
	estimate += configSize
	if estimate > MaxBackup {
		return fmt.Errorf("site exceeds backup size bound")
	}
	// du allocates blocks; reserve extra metadata/temporary-copy headroom.
	if err = reserve(filepath.Dir(destination), 4*estimate+1024*1024*1024); err != nil {
		return err
	}
	for service, id := range ids {
		source := "/mosquitto/data/."
		if service == "historian" {
			source = "/storage/."
		}
		target := filepath.Join(scratch, service)
		if err = os.Mkdir(target, 0700); err != nil {
			return err
		}
		if _, err = m.run(ctx, m.Dir, []string{"cp", id + ":" + source, target}, environment(manifest)); err != nil {
			return err
		}
	}
	if manifest.Profile == "secure" {
		if _, err = m.run(ctx, m.Dir, []string{"cp", ids["broker"] + ":/credentials/broker/roles.json", filepath.Join(scratch, "broker-policy.json")}, environment(manifest)); err != nil {
			return err
		}
	}
	if err = copyTree(m.Dir, filepath.Join(scratch, "manager")); err != nil {
		return err
	}
	for name, path := range manifest.HostState {
		if err = copyTree(path, filepath.Join(scratch, "host", name)); err != nil {
			return err
		}
	}
	digest, err := Archive(scratch, destination)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"archive": destination, "sha256": digest, "cold_copy_ms": time.Since(start).Milliseconds(), "revision": manifest.Revision, "boundary": "clean_component_shutdown_and_synced_host_stores", "secrets": "excluded_reprovision_before_restore"})
}
