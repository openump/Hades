// Package storage implements a single-owner, bounded, synced file store.
// Each record is atomically replaced; a failed sync is fatal to its caller.
package storage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const Overhead = 4096
const MaxRecord = 100 * 1024

var ErrFull = fmt.Errorf("store capacity or disk reserve reached")

type record struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}
type Store struct {
	Dir                  string
	Records              map[string]json.RawMessage
	Bytes                int
	maxRecords, maxBytes int
	reserve              uint64
	lock                 *os.File
}

func Open(dir string, maxRecords, maxBytes int, reserve uint64) (s *Store, err error) {
	if maxRecords < 1 || maxBytes < Overhead {
		return nil, fmt.Errorf("invalid store limits")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("store directory must be private mode 0700, without symlink")
	}
	fd, err := syscall.Open(filepath.Join(dir, ".lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "store lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("store already owned: %w", err)
	}
	s = &Store{dir, map[string]json.RawMessage{}, 0, maxRecords, maxBytes, reserve, lock}
	defer func() {
		if err != nil {
			lock.Close()
		}
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("unexpected store entry %s", entry.Name())
		}
		if entry.Name() == ".pending" {
			if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return nil, err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if info.Size() > MaxRecord || info.Mode().Perm() != 0600 {
			return nil, fmt.Errorf("invalid record size/permissions")
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var r record
		if err = json.Unmarshal(raw, &r); err != nil || r.Key == "" || !json.Valid(r.Value) || filename(r.Key) != entry.Name() {
			return nil, fmt.Errorf("corrupt store record %s", entry.Name())
		}
		if _, exists := s.Records[r.Key]; exists {
			return nil, fmt.Errorf("duplicate store key")
		}
		s.Records[r.Key] = r.Value
		s.Bytes += len(raw) + Overhead
	}
	if len(s.Records) > maxRecords || s.Bytes > maxBytes {
		return nil, ErrFull
	}
	return s, nil
}
func filename(key string) string { return fmt.Sprintf("%x.json", sha256.Sum256([]byte(key))) }
func encoded(key string, value json.RawMessage) ([]byte, error) {
	return json.Marshal(record{key, value})
}
func (s *Store) Available(extra int) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(s.Dir, &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < s.reserve+uint64(max(extra, 0)) {
		return ErrFull
	}
	return nil
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (s *Store) Put(key string, value json.RawMessage) error {
	if key == "" || !json.Valid(value) {
		return fmt.Errorf("invalid store record")
	}
	raw, err := encoded(key, value)
	if err != nil {
		return err
	}
	if len(raw) > MaxRecord {
		return ErrFull
	}
	old, exists := s.Records[key]
	oldSize := 0
	if exists {
		b, _ := encoded(key, old)
		oldSize = len(b) + Overhead
	}
	newSize := len(raw) + Overhead
	if (!exists && len(s.Records) >= s.maxRecords) || s.Bytes-oldSize+newSize > s.maxBytes {
		return ErrFull
	}
	if err = s.Available(newSize); err != nil {
		return err
	}
	// O_NOFOLLOW prevents an unexpected temporary symlink from being followed.
	fd, err := syscall.Open(filepath.Join(s.Dir, ".pending"), syscall.O_CREAT|syscall.O_TRUNC|syscall.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), "pending")
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(s.Dir, ".pending"), filepath.Join(s.Dir, filename(key))); err != nil {
		return err
	}
	if err = syncDir(s.Dir); err != nil {
		return err
	}
	s.Records[key] = append(json.RawMessage(nil), value...)
	s.Bytes += newSize - oldSize
	return nil
}
func (s *Store) Delete(key string) error {
	old, exists := s.Records[key]
	if !exists {
		return nil
	}
	if err := os.Remove(filepath.Join(s.Dir, filename(key))); err != nil {
		return err
	}
	if err := syncDir(s.Dir); err != nil {
		return err
	}
	raw, _ := encoded(key, old)
	s.Bytes -= len(raw) + Overhead
	delete(s.Records, key)
	return nil
}
func (s *Store) Close() error { return s.lock.Close() }
