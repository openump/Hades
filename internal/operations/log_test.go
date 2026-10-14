package operations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogRotatesWithinConfiguredBound(t *testing.T) {
	dir := t.TempDir()
	w, err := OpenLog(filepath.Join(dir, "service.log"), 32)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for i := 0; i < 10; i++ {
		if _, err = w.Write([]byte(strings.Repeat("x", 20))); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatal(len(entries))
	}
	for _, e := range entries {
		info, _ := e.Info()
		if info.Size() > 32 || info.Mode().Perm() != 0600 {
			t.Fatal(info)
		}
	}
}
