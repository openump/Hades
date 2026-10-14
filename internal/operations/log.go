// Package operations provides bounded optional diagnostic logs for host services.
package operations

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type Log struct {
	mu         sync.Mutex
	file       *os.File
	path       string
	bytes, max int
	lock       *os.File
}

func OpenLog(path string, limit int) (*Log, error) {
	if limit < 1 {
		return nil, fmt.Errorf("invalid log limit")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path+".lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "log lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, err
	}
	fd, err = syscall.Open(path, syscall.O_CREAT|syscall.O_APPEND|syscall.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		lock.Close()
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		f.Close()
		lock.Close()
		return nil, fmt.Errorf("log must be private regular file")
	}
	return &Log{file: f, path: path, bytes: int(info.Size()), max: limit, lock: lock}, nil
}
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	original := len(p)
	if len(p) > l.max {
		p = p[:l.max]
	}
	if l.bytes+len(p) > l.max {
		if err := l.file.Close(); err != nil {
			return 0, err
		}
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			return 0, err
		}
		fd, err := syscall.Open(l.path, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0600)
		if err != nil {
			return 0, err
		}
		l.file = os.NewFile(uintptr(fd), l.path)
		l.bytes = 0
	}
	n, err := l.file.Write(p)
	l.bytes += n
	if err != nil {
		return n, err
	}
	return original, nil
}
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.file.Close()
	l.lock.Close()
	return err
}
func LogFlag() *string {
	return flag.String("log-file", "", "optional private rotated diagnostic log (5 MiB x 2)")
}
func ConfigureLog(path string) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	w, err := OpenLog(path, 5*1024*1024)
	if err != nil {
		return nil, err
	}
	prior := log.Writer()
	log.SetOutput(w)
	return func() { log.SetOutput(prior); w.Close() }, nil
}
