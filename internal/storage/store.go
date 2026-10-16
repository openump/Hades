// Package storage implements a single-owner, bounded, synced WAL store.
// Records are appended to numbered segments and recovered through a compact
// in-memory index. Payload bytes are streamed from disk on demand.
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
	"strings"
	"syscall"
	"time"
)

const Overhead = 4096
const MaxRecord = 100 * 1024
const segmentPrefix = "segment-"
const segmentSuffix = ".wal"
const segmentMaxBytes = 16 * 1024 * 1024
const frameHeaderSize = 28

const (
	framePut    byte = 1
	frameDelete byte = 2
)

var ErrFull = fmt.Errorf("store capacity or disk reserve reached")

type entry struct {
	Segment   string
	Offset    int64
	KeyLen    uint32
	ValueLen  uint32
	TotalSize int
}

type Store struct {
	closed        bool
	Dir           string
	Records       map[string]entry
	Bytes         int
	maxRecords    int
	maxBytes      int
	reserve       uint64
	lock          *os.File
	segments      map[string]*os.File
	activeName    string
	active        *os.File
	activeSize    int64
	nextSegmentID int
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
	s = &Store{Dir: dir, Records: map[string]entry{}, maxRecords: maxRecords, maxBytes: maxBytes, reserve: reserve, lock: lock, segments: map[string]*os.File{}}
	defer func() {
		if err != nil && s != nil {
			for _, f := range s.segments {
				f.Close()
			}
			lock.Close()
		}
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.Name() == ".lock" {
			continue
		}
		if entry.Name() == ".pending" {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("unexpected store entry %s", entry.Name())
		}
		if _, ok := parseSegment(entry.Name()); !ok {
			return nil, fmt.Errorf("unexpected store entry %s", entry.Name())
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	if len(names) == 0 {
		name := segmentName(1)
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			return nil, err
		}
		s.segments[name] = f
		s.activeName = name
		s.active = f
		s.activeSize = 0
		s.nextSegmentID = 1
		s.Bytes = 0
		return s, nil
	}
	for i, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if info.Mode().Perm() != 0600 {
			return nil, fmt.Errorf("invalid segment permissions")
		}
		flags := os.O_RDWR
		if i == len(names)-1 {
			flags |= os.O_CREATE
		}
		f, err := os.OpenFile(path, flags, 0600)
		if err != nil {
			return nil, err
		}
		s.segments[name] = f
		if id, ok := parseSegment(name); ok && id > s.nextSegmentID {
			s.nextSegmentID = id
		}
		if i == len(names)-1 {
			s.activeName = name
			s.active = f
			s.activeSize = info.Size()
		}
		if err = s.replaySegment(name, f, i == len(names)-1); err != nil {
			for _, seg := range s.segments {
				seg.Close()
			}
			lock.Close()
			return nil, err
		}
	}
	if s.active == nil {
		if err = s.openNextSegment(); err != nil {
			for _, seg := range s.segments {
				seg.Close()
			}
			lock.Close()
			return nil, err
		}
	}
	if len(s.Records) > maxRecords || s.Bytes > maxBytes {
		return nil, ErrFull
	}
	return s, nil
}

func segmentName(id int) string { return fmt.Sprintf("%s%06d%s", segmentPrefix, id, segmentSuffix) }

func parseSegment(name string) (int, bool) {
	if !strings.HasPrefix(name, segmentPrefix) || !strings.HasSuffix(name, segmentSuffix) {
		return 0, false
	}
	var id int
	if _, err := fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(name, segmentPrefix), segmentSuffix), "%d", &id); err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

func (s *Store) openNextSegment() error {
	s.nextSegmentID++
	name := segmentName(s.nextSegmentID)
	path := filepath.Join(s.Dir, name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if s.active != nil {
		_ = s.active.Sync()
	}
	s.segments[name] = f
	s.activeName = name
	s.active = f
	info, err := f.Stat()
	if err != nil {
		return err
	}
	s.activeSize = info.Size()
	return nil
}

func (s *Store) replaySegment(name string, file *os.File, tolerateTail bool) error {
	var offset int64
	for {
		kind, size, key, value, err := readFrame(file, offset)
		if err == io.EOF {
			if tolerateTail && offset > 0 {
				return file.Truncate(offset)
			}
			return nil
		}
		if err != nil {
			if tolerateTail {
				return file.Truncate(offset)
			}
			return fmt.Errorf("corrupt store segment %s: %w", name, err)
		}
		switch kind {
		case framePut:
			if key == "" || !json.Valid(value) {
				if tolerateTail {
					return file.Truncate(offset)
				}
				return fmt.Errorf("corrupt store segment %s", name)
			}
			if old, exists := s.Records[key]; exists {
				s.Bytes -= old.TotalSize
			}
			logical := int(size) + Overhead
			s.Records[key] = entry{Segment: name, Offset: offset, KeyLen: uint32(len(key)), ValueLen: uint32(len(value)), TotalSize: logical}
			s.Bytes += logical
		case frameDelete:
			if old, exists := s.Records[key]; exists {
				s.Bytes -= old.TotalSize
				delete(s.Records, key)
			}
		default:
			if tolerateTail {
				return file.Truncate(offset)
			}
			return fmt.Errorf("corrupt store segment %s", name)
		}
		offset += size
	}
}

func frameSize(keyLen, valueLen int) int { return frameHeaderSize + keyLen + valueLen }

func readFrame(file *os.File, offset int64) (byte, int64, string, json.RawMessage, error) {
	header := make([]byte, frameHeaderSize)
	if _, err := file.ReadAt(header, offset); err != nil {
		return 0, 0, "", nil, err
	}
	if string(header[:4]) != "HWAL" {
		return 0, 0, "", nil, fmt.Errorf("bad frame magic")
	}
	kind := header[4]
	keyLen := binary.LittleEndian.Uint32(header[16:20])
	valueLen := binary.LittleEndian.Uint32(header[20:24])
	want := binary.LittleEndian.Uint32(header[24:28])
	body := make([]byte, int(keyLen)+int(valueLen))
	if _, err := file.ReadAt(body, offset+frameHeaderSize); err != nil {
		return 0, 0, "", nil, err
	}
	h := crc32.NewIEEE()
	h.Write([]byte{kind})
	h.Write(header[8:16])
	h.Write(body)
	if h.Sum32() != want {
		return 0, 0, "", nil, fmt.Errorf("bad frame checksum")
	}
	key := string(body[:keyLen])
	value := json.RawMessage(append([]byte(nil), body[keyLen:]...))
	return kind, int64(frameHeaderSize + len(body)), key, value, nil
}

func writeFrame(file *os.File, kind byte, key string, value json.RawMessage) (int64, error) {
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return 0, err
	}
	frame := make([]byte, frameHeaderSize+len(key)+len(value))
	copy(frame[:4], []byte("HWAL"))
	frame[4] = kind
	timestamp := time.Now().UTC().UnixNano()
	binary.LittleEndian.PutUint64(frame[8:16], uint64(timestamp))
	binary.LittleEndian.PutUint32(frame[16:20], uint32(len(key)))
	binary.LittleEndian.PutUint32(frame[20:24], uint32(len(value)))
	copy(frame[frameHeaderSize:frameHeaderSize+len(key)], key)
	copy(frame[frameHeaderSize+len(key):], value)
	h := crc32.NewIEEE()
	h.Write([]byte{kind})
	h.Write(frame[8:16])
	h.Write(frame[frameHeaderSize:])
	binary.LittleEndian.PutUint32(frame[24:28], h.Sum32())
	if _, err = file.Write(frame); err != nil {
		return 0, err
	}
	if err = file.Sync(); err != nil {
		return 0, err
	}
	return offset, nil
}

func (s *Store) recordValue(key string) (json.RawMessage, bool, error) {
	meta, exists := s.Records[key]
	if !exists {
		return nil, false, nil
	}
	file := s.segments[meta.Segment]
	if file == nil {
		var err error
		file, err = os.OpenFile(filepath.Join(s.Dir, meta.Segment), os.O_RDONLY, 0600)
		if err != nil {
			return nil, false, err
		}
		defer file.Close()
	}
	body := make([]byte, int(meta.ValueLen))
	if _, err := file.ReadAt(body, meta.Offset+frameHeaderSize+int64(meta.KeyLen)); err != nil {
		return nil, false, err
	}
	return json.RawMessage(append([]byte(nil), body...)), true, nil
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

func (s *Store) Put(key string, value json.RawMessage) error {
	if s.closed {
		return fmt.Errorf("store is closed")
	}
	if key == "" || !json.Valid(value) {
		return fmt.Errorf("invalid store record")
	}
	if len(key)+len(value)+frameHeaderSize > MaxRecord {
		return ErrFull
	}
	if _, exists := s.Records[key]; !exists && len(s.Records) >= s.maxRecords {
		return ErrFull
	}
	if err := s.Available(len(key) + len(value) + frameHeaderSize); err != nil {
		return err
	}
	if s.active == nil {
		if err := s.openNextSegment(); err != nil {
			return err
		}
	}
	frameBytes := frameSize(len(key), len(value))
	if s.activeSize+int64(frameBytes) > segmentMaxBytes {
		if err := s.openNextSegment(); err != nil {
			return err
		}
	}
	if s.Bytes+frameBytes+Overhead > s.maxBytes {
		return ErrFull
	}
	offset, err := writeFrame(s.active, framePut, key, value)
	if err != nil {
		return err
	}
	s.activeSize += int64(frameBytes)
	logical := frameBytes + Overhead
	if old, exists := s.Records[key]; exists {
		s.Bytes -= old.TotalSize
	}
	s.Bytes += logical
	s.Records[key] = entry{Segment: s.activeName, Offset: offset, KeyLen: uint32(len(key)), ValueLen: uint32(len(value)), TotalSize: logical}
	return nil
}

func (s *Store) Delete(key string) error {
	if s.closed {
		return fmt.Errorf("store is closed")
	}
	if _, exists := s.Records[key]; !exists {
		return nil
	}
	if s.active == nil {
		if err := s.openNextSegment(); err != nil {
			return err
		}
	}
	frameBytes := frameSize(len(key), 0)
	if s.activeSize+int64(frameBytes) > segmentMaxBytes {
		if err := s.openNextSegment(); err != nil {
			return err
		}
	}
	if s.Bytes+frameBytes+Overhead > s.maxBytes {
		return ErrFull
	}
	if _, err := writeFrame(s.active, frameDelete, key, nil); err != nil {
		return err
	}
	s.activeSize += int64(frameBytes)
	if old, exists := s.Records[key]; exists {
		s.Bytes -= old.TotalSize
	}
	delete(s.Records, key)
	return nil
}

func (s *Store) Read(key string) (json.RawMessage, bool, error) {
	return s.recordValue(key)
}

func (s *Store) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	for _, segment := range s.segments {
		segment.Close()
	}
	return s.lock.Close()
}

// Stats is called under the owning service's lock; free space is sampled live.
func (s *Store) Stats() map[string]any {
	var stat syscall.Statfs_t
	err := syscall.Statfs(s.Dir, &stat)
	var free uint64
	if err == nil {
		free = uint64(stat.Bavail) * uint64(stat.Bsize)
	}
	return map[string]any{"records": len(s.Records), "bytes": s.Bytes, "limit_records": s.maxRecords, "limit_bytes": s.maxBytes, "free_bytes": free, "reserve_bytes": s.reserve, "disk_reserve_ok": err == nil && free >= s.reserve, "disk_probe_ok": err == nil}
}
