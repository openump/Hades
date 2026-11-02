package release

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Tarball writes a reproducible .tar.gz of srcDir under prefix/: sorted
// entries, a fixed modification time, root ownership, normalized modes and a
// gzip header without name or time, so the same inputs give the same bytes on
// any host. Symlinks are kept as symlinks.
func Tarball(out, srcDir, prefix string, mtime time.Time) error {
	var paths []string
	err := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != srcDir {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(out) // never leave a truncated archive to be signed
		}
	}()
	gz, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	gz.Header.ModTime, gz.Header.Name, gz.Header.OS = time.Time{}, "", 255
	tw := tar.NewWriter(gz)
	for _, p := range paths {
		rel, _ := filepath.Rel(srcDir, p)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: filepath.ToSlash(filepath.Join(prefix, rel)), ModTime: mtime.UTC(), Uid: 0, Gid: 0, Uname: "root", Gname: "root", Format: tar.FormatPAX}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, target, 0o777
		case info.IsDir():
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, hdr.Name+"/", 0o755
		default:
			hdr.Typeflag, hdr.Size, hdr.Mode = tar.TypeReg, info.Size(), 0o644
			if info.Mode()&0o111 != 0 {
				hdr.Mode = 0o755
			}
		}
		// PAX records would carry access/change times; keep only what is set.
		hdr.AccessTime, hdr.ChangeTime = time.Time{}, time.Time{}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeReg {
			src, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, src)
			src.Close()
			if err != nil {
				return err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}
