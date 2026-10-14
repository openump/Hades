package storage

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSyncedBoundsAndExclusiveRestart(t *testing.T) {
	dir := t.TempDir() + "/store"
	s, err := Open(dir, 2, 10000, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(dir, 2, 10000, 0); err == nil {
		t.Fatal("second owner accepted")
	}
	if err = s.Put("one", json.RawMessage(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.Put("two", json.RawMessage(`{"value":2}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.Put("three", json.RawMessage(`{}`)); err == nil {
		t.Fatal("record limit ignored")
	}
	s.Close()
	s, err = Open(dir, 2, 10000, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Records) != 2 {
		t.Fatal("restart lost intake")
	}
	if err = s.Delete("one"); err != nil {
		t.Fatal(err)
	}
	if err = s.Put("three", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0700 {
		t.Fatal(info.Mode())
	}
}
func TestCorruptionAndByteCapacity(t *testing.T) {
	dir := t.TempDir() + "/store"
	s, err := Open(dir, 2, 4100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Put("one", json.RawMessage(`{"value":1}`)); err == nil {
		t.Fatal("byte cap ignored")
	}
	s.Close()
	os.WriteFile(dir+"/corrupt.json", []byte("broken"), 0600)
	if _, err = Open(dir, 2, 4100, 0); err == nil {
		t.Fatal("ignored corrupt record")
	}
}
