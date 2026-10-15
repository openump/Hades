package site

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestoresExactBytesAndRejectsCorruption(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(root+"/transfer", 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"message_id":"original","observed_at":"unchanged"}`)
	if err := os.WriteFile(root+"/transfer/record.json", raw, 0600); err != nil {
		t.Fatal(err)
	}
	archive := t.TempDir() + "/backup.tar"
	digest, err := Archive(root, archive)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir() + "/restored"
	if err = Restore(archive, target, digest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target + "/transfer/record.json")
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal(err, string(got))
	}
	if err = Restore(archive, target, digest); err == nil {
		t.Fatal("existing restore overwritten")
	}
	data, _ := os.ReadFile(archive)
	data[600] ^= 1
	os.WriteFile(archive, data, 0600)
	badTarget := t.TempDir() + "/corrupt"
	if err = Restore(archive, badTarget, digest); err == nil {
		t.Fatal("corruption accepted")
	}
	if _, err = os.Stat(badTarget); !os.IsNotExist(err) {
		t.Fatal("invalid restore exposed")
	}
}
func TestBackupRejectsSecretsSymlinksAndTraversal(t *testing.T) {
	for _, kind := range []string{"key", "symlink"} {
		root := t.TempDir()
		if kind == "key" {
			os.WriteFile(root+"/ca.key", []byte("secret"), 0600)
		} else {
			os.Symlink("/etc/passwd", root+"/link")
		}
		if _, err := Archive(root, t.TempDir()+"/bad.tar"); err == nil {
			t.Fatal("unsafe backup accepted", kind)
		}
	}
	for _, path := range []string{"../escape", "/absolute", "safe/../../escape", "a\\b"} {
		var buffer bytes.Buffer
		writer := tar.NewWriter(&buffer)
		writer.WriteHeader(&tar.Header{Name: path, Mode: 0600, Size: 1, Typeflag: tar.TypeReg})
		writer.Write([]byte("x"))
		writer.Close()
		file := t.TempDir() + "/malicious.tar"
		os.WriteFile(file, buffer.Bytes(), 0600)
		digest := fmt.Sprintf("%x", sha256.Sum256(buffer.Bytes()))
		if err := Restore(file, t.TempDir()+"/target", digest); err == nil {
			t.Fatal("traversal accepted", path)
		}
	}
}
func TestBackupRequiresStoppedHostStore(t *testing.T) {
	dir := t.TempDir() + "/owned"
	store, err := Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manifest := Manifest{HostState: map[string]string{"history-intake": filepath.Join(dir, "control")}}
	locks, err := lockHostStores(manifest)
	if err == nil {
		for _, lock := range locks {
			lock.Close()
		}
		t.Fatal("live store accepted")
	}
}

func TestBackupCannotRecurseIntoItsOwnDestination(t *testing.T) {
	root := t.TempDir()
	if _, err := Archive(root, root+"/nested.tar"); err == nil {
		t.Fatal("recursive archive target accepted")
	}
}

func TestBackupMayUseSourceParentWithoutRecursing(t *testing.T) {
	parent := t.TempDir()
	source := parent + "/source"
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source+"/record.json", []byte(`{"owned":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Archive(source, parent+"/backup.tar"); err != nil {
		t.Fatal("parent is outside source, not recursive", err)
	}
}
