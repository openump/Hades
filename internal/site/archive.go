package site

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const MaxBackup int64 = 32 * 1024 * 1024 * 1024
const MaxBackupFiles = 10000

func forbidden(path string) bool {
	return strings.HasSuffix(path, ".key") || strings.HasSuffix(path, ".pem") || strings.Contains(path, "credentials/")
}
func reserve(path string, size int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < uint64(max(0, size))+128*1024*1024 {
		return fmt.Errorf("backup free-disk reserve insufficient")
	}
	return nil
}
func files(root string) ([]string, int64, error) {
	var names []string
	var size int64
	st, err := os.Lstat(root)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("source must be a real directory")
	}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if !safe(relative) || d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe backup entry: %s", relative)
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == ".lock" {
			return nil
		}
		if !d.Type().IsRegular() || forbidden(relative) || relative == "backup-index.json" {
			return fmt.Errorf("unsupported/secret backup entry: %s", relative)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		size += info.Size()
		names = append(names, relative)
		if size > MaxBackup || len(names) > MaxBackupFiles {
			return fmt.Errorf("backup limit exceeded")
		}
		return nil
	})
	return names, size, err
}

// Archive writes a cold, private tar with an exact per-file digest index. The
// caller supplies stopped component directories and holds all application locks.
func Archive(root, destination string) (digest string, err error) {
	sourceRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return "", err
	}
	if within(sourceRoot, parent) {
		return "", fmt.Errorf("backup destination must be outside source tree")
	}
	names, size, err := files(root)
	if err != nil {
		return "", err
	}
	if err = reserve(filepath.Dir(destination), size+int64(len(names))*2048+16*1024*1024); err != nil {
		return "", err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		output.Close()
		if !complete {
			os.Remove(destination)
		}
	}()
	hasher := sha256.New()
	writer := tar.NewWriter(io.MultiWriter(output, hasher))
	index := map[string]string{}
	for _, name := range names {
		file, err := os.Open(filepath.Join(root, name))
		if err != nil {
			return "", err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return "", err
		}
		if err = writer.WriteHeader(&tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Typeflag: tar.TypeReg, Size: info.Size()}); err != nil {
			file.Close()
			return "", err
		}
		checksum := sha256.New()
		n, err := io.CopyN(io.MultiWriter(writer, checksum), file, info.Size())
		file.Close()
		if err != nil || n != info.Size() {
			return "", fmt.Errorf("source changed during backup: %s", name)
		}
		index[name] = fmt.Sprintf("%x", checksum.Sum(nil))
	}
	raw, _ := json.Marshal(index)
	if err = writer.WriteHeader(&tar.Header{Name: "backup-index.json", Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(raw))}); err != nil {
		return "", err
	}
	if _, err = writer.Write(raw); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	if err = output.Sync(); err != nil {
		return "", err
	}
	if err = output.Close(); err != nil {
		return "", err
	}
	if err = syncDir(filepath.Dir(destination)); err != nil {
		return "", err
	}
	complete = true
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

// Restore accepts only a new directory and a separately trusted archive digest.
// Files become visible at target only after full validation and directory sync.
func Restore(archive, target, digest string) (err error) {
	if !hash.MatchString(digest) {
		return fmt.Errorf("trusted backup SHA256 required")
	}
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		return fmt.Errorf("restore target must not exist")
	}
	source, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBackup+32*1024*1024 {
		return fmt.Errorf("invalid backup file size/type")
	}
	if err = reserve(filepath.Dir(target), info.Size()); err != nil {
		return err
	}
	checksum := sha256.New()
	if _, err = io.Copy(checksum, io.LimitReader(source, MaxBackup+32*1024*1024+1)); err != nil {
		return err
	}
	if fmt.Sprintf("%x", checksum.Sum(nil)) != digest {
		return fmt.Errorf("backup SHA256 mismatch")
	}
	if _, err = source.Seek(0, 0); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(target), ".hades-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	reader := tar.NewReader(source)
	index := map[string]string{}
	var expected map[string]string
	var total int64
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if !safe(header.Name) || header.Typeflag != tar.TypeReg || forbidden(header.Name) || header.Size < 0 || (header.Mode != 0600 && header.Mode != 0644 && header.Mode != 0700 && header.Mode != 0755) {
			return fmt.Errorf("unsafe restore entry: %s", header.Name)
		}
		if header.Name == "backup-index.json" {
			if expected != nil || header.Size > 4*1024*1024 {
				return fmt.Errorf("invalid backup index")
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &expected); err != nil {
				return err
			}
			continue
		}
		if _, ok := index[header.Name]; ok {
			return fmt.Errorf("duplicate archive file")
		}
		total += header.Size
		if total > MaxBackup || len(index) >= MaxBackupFiles {
			return fmt.Errorf("restore limit exceeded")
		}
		path := filepath.Join(temporary, header.Name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		checksum := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(file, checksum), reader)
		modeErr := file.Chmod(os.FileMode(header.Mode))
		syncErr := file.Sync()
		closeErr := file.Close()
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
		index[header.Name] = fmt.Sprintf("%x", checksum.Sum(nil))
	}
	if expected == nil || len(expected) != len(index) {
		return fmt.Errorf("backup index missing/incomplete")
	}
	for name, digest := range expected {
		if index[name] != digest {
			return fmt.Errorf("backup file digest mismatch: %s", name)
		}
	}
	if err = filepath.WalkDir(temporary, func(path string, d os.DirEntry, err error) error {
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
	if err = os.Rename(temporary, target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}
func lockHostStores(manifest Manifest) (locks []*os.File, err error) {
	defer func() {
		if err != nil {
			for _, lock := range locks {
				lock.Close()
			}
		}
	}()
	for name, path := range manifest.HostState {
		st, e := os.Lstat(path)
		if e != nil || !st.IsDir() || st.Mode().Perm() != 0700 || st.Mode()&os.ModeSymlink != 0 {
			return locks, fmt.Errorf("host store %s missing or not private", name)
		}
		fd, e := syscall.Open(filepath.Join(path, ".lock"), syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return locks, fmt.Errorf("host store %s lacks ownership lock", name)
		}
		lock := os.NewFile(uintptr(fd), name)
		if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
			lock.Close()
			return locks, fmt.Errorf("stop host adapter owning %s before backup", name)
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && (relative == "." || (!strings.HasPrefix(relative, "../") && !filepath.IsAbs(relative)))
}
