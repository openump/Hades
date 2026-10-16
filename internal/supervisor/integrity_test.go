package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrityCacheAvoidsUnchangedPayloadReads(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "binary")
	config := filepath.Join(dir, "config")
	if err := os.WriteFile(binary, []byte("reviewed executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("reviewed config"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	configDigest, err := hashFile(config)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Revision: "test", Binary: binary, BinarySHA256: digest, Directory: dir, State: filepath.Join(dir, "state"), Ready: "ready", LogBytes: 512, Files: map[string]string{config: configDigest}}
	calls := 0
	hash := func(path string) (string, error) { calls++; return hashFile(path) }
	cache, err := newIntegrity(cfg, hash)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("startup did not verify every artifact", calls)
	}
	for range 50 {
		if err = cache.check(false); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("unchanged probes reread payloads", calls)
	}
	if err = cache.check(true); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatal("periodic verification skipped", calls)
	}
}

func TestIntegrityDetectsArtifactChanges(t *testing.T) {
	for _, target := range []string{"binary", "config"} {
		for _, change := range []string{"in-place", "size", "permission", "rename", "symlink", "restored-mtime"} {
			t.Run(target+"/"+change, func(t *testing.T) {
				dir := t.TempDir()
				binary := filepath.Join(dir, "binary")
				config := filepath.Join(dir, "config")
				for _, path := range []string{binary, config} {
					if err := os.WriteFile(path, []byte("approved"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				digest, _ := hashFile(binary)
				cfg := Config{Revision: "test", Binary: binary, BinarySHA256: digest, Directory: dir, State: filepath.Join(dir, "state"), Ready: "ready", LogBytes: 512, Files: map[string]string{config: digest}}
				cache, err := newIntegrity(cfg, hashFile)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, target)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "in-place", "restored-mtime":
					err = os.WriteFile(path, []byte("tampered"), 0600)
					if err == nil && change == "restored-mtime" {
						err = os.Chtimes(path, info.ModTime(), info.ModTime())
					}
				case "size":
					err = os.WriteFile(path, []byte("different size"), 0600)
				case "permission":
					err = os.Chmod(path, 0644)
				case "rename":
					next := path + ".new"
					err = os.WriteFile(next, []byte("tampered"), 0600)
					if err == nil {
						err = os.Rename(next, path)
					}
				case "symlink":
					err = os.Rename(path, path+".old")
					if err == nil {
						err = os.Symlink(path+".old", path)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = cache.check(false); err == nil {
					t.Fatal("changed artifact accepted")
				}
				if change != "permission" && change != "symlink" {
					if err = Validate(cfg); err == nil {
						t.Fatal("full verification accepted tampered artifact")
					}
				}
				if err = cache.check(false); err == nil {
					t.Fatal("failure was not latched")
				}
			})
		}
	}
}

func TestPeriodicIntegrityDoesNotTrustMetadataAlone(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "binary")
	if err := os.WriteFile(binary, []byte("approved"), 0700); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Revision: "test", Binary: binary, BinarySHA256: digest, Directory: dir, State: filepath.Join(dir, "state"), Ready: "ready", LogBytes: 512}
	changed := false
	cache, err := newIntegrity(cfg, func(path string) (string, error) {
		if changed {
			return strings.Repeat("0", 64), nil
		}
		return hashFile(path)
	})
	if err != nil {
		t.Fatal(err)
	}
	changed = true
	if err = cache.check(false); err != nil {
		t.Fatal("unchanged metadata did not use cache", err)
	}
	if err = cache.check(true); err == nil {
		t.Fatal("forced sweep trusted metadata")
	}
}
