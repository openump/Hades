package storage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// WAL is an opt-in v2 store foundation. Owners serialize calls. A transaction is
// one checksummed frame and one fsync; no durable success precedes that sync.
// The resident index contains only key/offset/length, never payload copies.
// Existing v1 stores are never opened or migrated implicitly.
type Mutation struct {
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value,omitempty"`
	Delete bool            `json:"delete,omitempty"`
}
type walLocation struct {
	Segment int
	Offset  int64
	Length  int
	Item    int
}
type WAL struct {
	dir                         string
	lock                        *os.File
	file                        *os.File
	generation, segment         int
	index                       map[string]walLocation
	maxRecords, maxBytes, bytes int
	reserve                     uint64
	physical                    int64
	failed, closed              bool
	Syncs                       uint64
}

const walSegmentBytes = 4 * 1024 * 1024
const walFrameBytes = 1024 * 1024

func walName(generation, segment int) string {
	return fmt.Sprintf("g%08d-%08d.wal", generation, segment)
}
func OpenWAL(dir string, maxRecords, maxBytes int, reserve uint64) (w *WAL, err error) {
	if maxRecords < 1 || maxRecords > 100000 || maxBytes < 4096 || maxBytes > 256*1024*1024 {
		return nil, fmt.Errorf("invalid WAL limits")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private real WAL directory required")
	}
	fd, err := syscall.Open(filepath.Join(dir, ".lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "WAL lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, err
	}
	w = &WAL{dir: dir, lock: lock, index: map[string]walLocation{}, maxRecords: maxRecords, maxBytes: maxBytes, reserve: reserve, generation: 1, segment: 1}
	opened := w
	defer func() {
		if err != nil {
			if opened.file != nil {
				opened.file.Close()
			}
			lock.Close()
		}
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) == 1 {
		if err = w.publishGeneration(1); err != nil {
			return nil, err
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".lock" || name == "FORMAT" || name == ".format-pending" {
			continue
		}
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, "g") || !strings.HasSuffix(name, ".wal") || len(name) != 22 {
			return nil, fmt.Errorf("unexpected WAL entry; v1 migration must be explicit")
		}
	}
	formatInfo, formatErr := os.Lstat(filepath.Join(dir, "FORMAT"))
	if formatErr != nil || !formatInfo.Mode().IsRegular() || formatInfo.Mode().Perm() != 0600 || formatInfo.Size() > 64 {
		return nil, fmt.Errorf("invalid private WAL format manifest")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "FORMAT"))
	if err != nil {
		return nil, fmt.Errorf("explicit v2 FORMAT required: %w", err)
	}
	if _, err = fmt.Sscanf(string(raw), "openump-wal/2 %d", &w.generation); err != nil || w.generation < 1 {
		return nil, fmt.Errorf("invalid WAL format")
	}
	paths, err := filepath.Glob(filepath.Join(dir, fmt.Sprintf("g%08d-*.wal", w.generation)))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	for i, path := range paths {
		name := filepath.Base(path)
		seg, parseErr := strconv.Atoi(strings.TrimSuffix(strings.Split(name, "-")[1], ".wal"))
		if parseErr != nil || seg != i+1 {
			return nil, fmt.Errorf("noncontiguous WAL segments")
		}
		w.segment = seg
		if err = w.recover(path, i == len(paths)-1); err != nil {
			return nil, err
		}
	}
	if string(raw) != fmt.Sprintf("openump-wal/2 %d\n", w.generation) {
		return nil, fmt.Errorf("invalid WAL format bytes")
	}
	if len(w.index) > maxRecords || w.bytes > maxBytes {
		return nil, ErrFull
	}
	if err = w.openSegment(); err != nil {
		return nil, err
	}
	return w, nil
}
func (w *WAL) publishGeneration(g int) error {
	path := filepath.Join(w.dir, ".format-pending")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_TRUNC|syscall.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	_, err = fmt.Fprintf(f, "openump-wal/2 %d\n", g)
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
	if err = os.Rename(path, filepath.Join(w.dir, "FORMAT")); err != nil {
		return err
	}
	return syncDir(w.dir)
}
func (w *WAL) openSegment() error {
	path := filepath.Join(w.dir, walName(w.generation, w.segment))
	_, statErr := os.Lstat(path)
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_APPEND|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	w.file = os.NewFile(uintptr(fd), path)
	info, err := w.file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("private regular WAL segment required")
	}
	if os.IsNotExist(statErr) {
		return syncDir(w.dir)
	}
	return nil
}
func decodeFrame(raw []byte) ([]Mutation, error) {
	var values []Mutation
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if len(values) < 1 || len(values) > 256 {
		return nil, fmt.Errorf("invalid WAL transaction")
	}
	for _, v := range values {
		if v.Key == "" || len(v.Key) > 512 || (!v.Delete && (!json.Valid(v.Value) || len(v.Value) > MaxRecord)) {
			return nil, fmt.Errorf("invalid WAL mutation")
		}
	}
	return values, nil
}
func (w *WAL) recover(path string, last bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("invalid WAL segment")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	offset := int64(0)
	for {
		var header [8]byte
		n, err := io.ReadFull(f, header[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			if last && (err == io.ErrUnexpectedEOF) {
				if err = f.Truncate(offset); err != nil {
					return err
				}
				return f.Sync()
			}
			return fmt.Errorf("torn sealed WAL header: %w", err)
		}
		_ = n
		size := int(binary.BigEndian.Uint32(header[:4]))
		if size < 1 || size > walFrameBytes {
			return fmt.Errorf("corrupt WAL frame size")
		}
		raw := make([]byte, size)
		_, err = io.ReadFull(f, raw)
		if err != nil {
			if last && (err == io.EOF || err == io.ErrUnexpectedEOF) {
				if err = f.Truncate(offset); err != nil {
					return err
				}
				return f.Sync()
			}
			return fmt.Errorf("torn sealed WAL body")
		}
		if crc32.ChecksumIEEE(raw) != binary.BigEndian.Uint32(header[4:]) {
			return fmt.Errorf("corrupt WAL checksum")
		}
		values, err := decodeFrame(raw)
		if err != nil {
			return err
		}
		w.apply(values, w.segment, offset, size)
		offset += int64(8 + size)
		w.physical += int64(8 + size)
		if len(w.index) > w.maxRecords || w.bytes > w.maxBytes || w.physical > int64(w.maxBytes)*2 {
			return ErrFull
		}
	}
	return nil
}
func (w *WAL) apply(values []Mutation, segment int, offset int64, size int) {
	for i, v := range values {
		if old, ok := w.index[v.Key]; ok {
			w.bytes -= old.Length + Overhead
		}
		if v.Delete {
			delete(w.index, v.Key)
		} else {
			w.index[v.Key] = walLocation{segment, offset, len(v.Value), i}
			w.bytes += len(v.Value) + Overhead
		}
	}
}
func (w *WAL) Commit(values []Mutation) error {
	if w.closed || w.failed {
		return fmt.Errorf("WAL closed or poisoned after write failure")
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if len(raw) > walFrameBytes {
		return ErrFull
	}
	if _, err = decodeFrame(raw); err != nil {
		return err
	}
	count, charged := len(w.index), w.bytes
	seen := map[string]bool{}
	for _, v := range values {
		if seen[v.Key] {
			return fmt.Errorf("duplicate transaction key")
		}
		seen[v.Key] = true
		if old, ok := w.index[v.Key]; ok {
			count--
			charged -= old.Length + Overhead
		}
		if !v.Delete {
			count++
			charged += len(v.Value) + Overhead
		}
	}
	if count > w.maxRecords || charged > w.maxBytes || w.physical+int64(len(raw)+8) > int64(w.maxBytes)*2 {
		return ErrFull
	}
	var stat syscall.Statfs_t
	if err = syscall.Statfs(w.dir, &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < w.reserve+uint64(len(raw)+8) {
		return ErrFull
	}
	info, err := w.file.Stat()
	if err != nil {
		return err
	}
	if info.Size()+int64(len(raw)+8) > walSegmentBytes {
		if err = w.file.Close(); err != nil {
			return err
		}
		w.segment++
		if err = w.openSegment(); err != nil {
			w.failed = true
			return err
		}
		info, err = w.file.Stat()
		if err != nil {
			return err
		}
	}
	frame := make([]byte, 8+len(raw))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(raw)))
	binary.BigEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(raw))
	copy(frame[8:], raw)
	n, err := w.file.Write(frame)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = w.file.Sync()
		w.Syncs++
	}
	if err != nil {
		w.failed = true
		return err
	}
	w.apply(values, w.segment, info.Size(), len(raw))
	w.physical += int64(len(frame))
	return nil
}
func (w *WAL) Get(key string) (json.RawMessage, error) {
	if w.closed || w.failed {
		return nil, fmt.Errorf("WAL unavailable")
	}
	loc, ok := w.index[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(w.dir, walName(w.generation, loc.Segment)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var header [8]byte
	if _, err = f.ReadAt(header[:], loc.Offset); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint32(header[:4]))
	if size < 1 || size > walFrameBytes {
		return nil, fmt.Errorf("corrupt WAL size")
	}
	raw := make([]byte, size)
	if _, err = f.ReadAt(raw, loc.Offset+8); err != nil {
		return nil, err
	}
	if crc32.ChecksumIEEE(raw) != binary.BigEndian.Uint32(header[4:]) {
		return nil, fmt.Errorf("corrupt WAL checksum")
	}
	values, err := decodeFrame(raw)
	if err != nil || loc.Item >= len(values) {
		return nil, fmt.Errorf("invalid WAL location")
	}
	v := values[loc.Item]
	if v.Key != key || v.Delete {
		return nil, fmt.Errorf("WAL index mismatch")
	}
	return v.Value, nil
}
func (w *WAL) Keys() []string {
	keys := make([]string, 0, len(w.index))
	for k := range w.index {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Compact copies at most the configured live byte/record limits. The old
// generation stays authoritative until the new generation's manifest is synced.
// Call only in a planned maintenance window; live compaction is not implicit.
func (w *WAL) Compact() error {
	if w.closed || w.failed {
		return fmt.Errorf("WAL unavailable")
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(w.dir, &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < w.reserve+uint64(w.maxBytes)*2 {
		return ErrFull
	}
	next := *w
	next.generation++
	next.segment = 1
	next.index = map[string]walLocation{}
	next.bytes = 0
	next.physical = 0
	next.file = nil
	stale, _ := filepath.Glob(filepath.Join(w.dir, fmt.Sprintf("g%08d-*.wal", next.generation)))
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	if err := next.openSegment(); err != nil {
		return err
	}
	for _, key := range w.Keys() {
		raw, err := w.Get(key)
		if err != nil {
			next.file.Close()
			return err
		}
		if err = next.Commit([]Mutation{{Key: key, Value: raw}}); err != nil {
			next.file.Close()
			return err
		}
	}
	if err := next.publishGeneration(next.generation); err != nil {
		next.file.Close()
		w.failed = true
		return err
	}
	old := w.generation
	w.file.Close()
	*w = next
	paths, _ := filepath.Glob(filepath.Join(w.dir, fmt.Sprintf("g%08d-*.wal", old)))
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return syncDir(w.dir)
}
func (w *WAL) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	err := w.file.Close()
	other := w.lock.Close()
	if err != nil {
		return err
	}
	return other
}
