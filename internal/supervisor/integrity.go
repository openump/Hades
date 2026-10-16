package supervisor

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// fileIdentity includes ctime so restoring mtime cannot hide an in-place edit.
// Lstat rejects symlinks on every probe, including cache hits.
type fileIdentity struct {
	device, inode     uint64
	size              int64
	mode              os.FileMode
	uid, gid          uint32
	modified, changed time.Time
}

func artifactIdentity(path string) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Size() > 512*1024*1024 || info.Mode().Perm()&0022 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
		return fileIdentity{}, fmt.Errorf("artifact must be bounded regular file owned by root/service user without group/world writes")
	}
	return fileIdentity{uint64(st.Dev), uint64(st.Ino), info.Size(), info.Mode(), st.Uid, st.Gid, info.ModTime(), changeTime(st)}, nil
}

type verifiedArtifact struct {
	identity fileIdentity
	digest   string
}
type integrity struct {
	mu      sync.Mutex
	files   map[string]verifiedArtifact
	hash    func(string) (string, error)
	failure error
}

func newIntegrity(cfg Config, hash func(string) (string, error)) (*integrity, error) {
	cache := &integrity{files: map[string]verifiedArtifact{}, hash: hash}
	err := validate(cfg, func(path string) (string, error) {
		before, err := artifactIdentity(path)
		if err != nil {
			return "", err
		}
		digest, err := hash(path)
		if err != nil {
			return "", err
		}
		after, err := artifactIdentity(path)
		if err != nil {
			return "", err
		}
		if before != after {
			return "", fmt.Errorf("artifact changed during verification")
		}
		cache.files[path] = verifiedArtifact{after, digest}
		return digest, nil
	})
	return cache, err
}

// Any observed integrity failure remains unhealthy until the supervisor restarts.
// This prevents a brief known-bad artifact from disappearing between probes.
func (c *integrity) check(force bool) (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return c.failure
	}
	defer func() {
		if err != nil {
			c.failure = err
		}
	}()
	for path, verified := range c.files {
		current, e := artifactIdentity(path)
		if e != nil {
			return e
		}
		if current.mode != verified.identity.mode || current.uid != verified.identity.uid || current.gid != verified.identity.gid {
			return fmt.Errorf("artifact permissions or ownership changed: %s", path)
		}
		if !force && current == verified.identity {
			continue
		}
		digest, e := c.hash(path)
		if e != nil {
			return e
		}
		after, e := artifactIdentity(path)
		if e != nil {
			return e
		}
		if current != after || digest != verified.digest {
			return fmt.Errorf("artifact digest/identity changed: %s", path)
		}
		c.files[path] = verifiedArtifact{after, verified.digest}
	}
	return nil
}
