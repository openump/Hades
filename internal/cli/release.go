package cli

import (
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/openump/Hades/internal/compliance"
	"github.com/openump/Hades/internal/release"
)

// Release runs the release tooling: keygen, sign, verify, notices, sbom.
func Release(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: hades release keygen|pubkey|sign|sign-file|verify|notices|sbom|tarball [flags]")
		return 2
	}
	fs := flag.NewFlagSet("hades release "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", ".", "directory holding the artifacts and checksums.txt")
	key := fs.String("key", "", "Ed25519 release private key (PKCS#8 PEM, private file)")
	pub := fs.String("pub", "", "Ed25519 release public key (SPKI PEM)")
	binary := fs.String("binary", "", "hades binary to inventory (notices, sbom)")
	out := fs.String("out", "", "output file (notices, sbom, tarball)")
	src := fs.String("src", "", "directory to archive (tarball)")
	prefix := fs.String("prefix", "", "top-level directory inside the archive (tarball)")
	file := fs.String("file", "", "file to sign (sign-file)")
	version := fs.String("version", "", "release version recorded in the SBOM (sbom)")
	goroot := fs.String("goroot", "", "Go toolchain GOROOT for the standard library notice (notices; default go env GOROOT)")
	epoch := fs.Int64("epoch", 0, "fixed modification time, Unix seconds (tarball; SOURCE_DATE_EPOCH)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	err := func() error {
		switch args[0] {
		case "keygen":
			if *key == "" || *pub == "" {
				return fmt.Errorf("-key and -pub are required")
			}
			return release.GenerateKey(*key, *pub)
		case "sign":
			// Sign every regular file in dir except the checksums and signature.
			priv, err := release.LoadPrivateKey(*key)
			if err != nil {
				return err
			}
			entries, err := os.ReadDir(*dir)
			if err != nil {
				return err
			}
			var names []string
			for _, e := range entries {
				if e.Type().IsRegular() && e.Name() != "checksums.txt" && e.Name() != "checksums.txt.sig" {
					names = append(names, e.Name())
				}
			}
			sums, err := release.Checksums(*dir, names)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(*dir, "checksums.txt"), sums, 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(*dir, "checksums.txt.sig"), release.Sign(priv, sums), 0o644)
		case "sign-file":
			// Sign one file (for example a bundle checksums.txt with subpaths).
			priv, err := release.LoadPrivateKey(*key)
			if err != nil {
				return err
			}
			raw, err := os.ReadFile(*file)
			if err != nil {
				return err
			}
			return os.WriteFile(*file+".sig", release.Sign(priv, raw), 0o644)
		case "pubkey":
			// Print the SPKI PEM public key of a release private key.
			priv, err := release.LoadPrivateKey(*key)
			if err != nil {
				return err
			}
			raw, err := release.PublicKeyPEM(priv.Public().(ed25519.PublicKey))
			if err != nil {
				return err
			}
			_, err = stdout.Write(raw)
			return err
		case "verify":
			pk, err := release.LoadPublicKey(*pub)
			if err != nil {
				return err
			}
			sums, err := os.ReadFile(filepath.Join(*dir, "checksums.txt"))
			if err != nil {
				return err
			}
			sig, err := os.ReadFile(filepath.Join(*dir, "checksums.txt.sig"))
			if err != nil {
				return err
			}
			if err := release.Verify(pk, *dir, sums, sig); err != nil {
				return err
			}
			fmt.Fprintln(stdout, "release verified: signature and every checksum match")
			return nil
		case "tarball":
			if *out == "" || *src == "" || *prefix == "" {
				return fmt.Errorf("-out, -src and -prefix are required")
			}
			return release.Tarball(*out, *src, *prefix, time.Unix(*epoch, 0))
		case "notices", "sbom":
			raw, err := os.ReadFile(*binary)
			if err != nil {
				return err
			}
			bom, err := compliance.BinarySBOM(raw, *version)
			if err != nil {
				return err
			}
			var text []byte
			if args[0] == "sbom" {
				text, _ = json.MarshalIndent(bom, "", "  ")
				text = append(text, '\n')
			} else {
				modcache := os.Getenv("GOMODCACHE")
				if modcache == "" {
					return fmt.Errorf("GOMODCACHE must name the Go module cache (go env GOMODCACHE)")
				}
				root := *goroot
				if root == "" {
					root = runtime.GOROOT()
				}
				notices, err := release.Notices(bom, modcache, root)
				if err != nil {
					return err
				}
				text = []byte(notices)
			}
			if *out == "" {
				_, err = stdout.Write(text)
				return err
			}
			return os.WriteFile(*out, text, 0o644)
		}
		return fmt.Errorf("unknown release command %q", args[0])
	}()
	if err != nil {
		fmt.Fprintln(stderr, "hades release:", err)
		return 1
	}
	return 0
}
