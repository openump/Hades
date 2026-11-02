package release

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openump/Hades/internal/compliance"
)

// licenseFiles are the names a module's license text may use, in order.
var licenseFiles = []string{"LICENSE", "LICENSE.txt", "LICENSE.md", "LICENCE", "COPYING", "LICENSE-MIT", "LICENSE-APACHE"}

// Notices renders THIRD_PARTY_NOTICES.md for a binary from its SBOM: every
// linked module with its version, reviewed license and full license text
// from the module cache. A linked module with no license text found fails,
// so a release can never ship without a notice for something it contains.
func Notices(bom *compliance.BOM, modcache, goroot string) (string, error) {
	var b strings.Builder
	b.WriteString("# Third-party notices\n\n")
	b.WriteString("Hades is licensed under Apache-2.0 (see LICENSE). The binary statically\n")
	b.WriteString("links the Go standard library and runtime, and the third-party Go modules\n")
	b.WriteString("listed below from its embedded build information (the same inventory as\n")
	b.WriteString("sbom.cdx.json).\n")
	// The Go standard library and runtime are in every binary (BSD-3-Clause).
	goLicense, err := os.ReadFile(filepath.Join(goroot, "LICENSE"))
	if err != nil {
		return "", fmt.Errorf("Go standard library license: %w (pass the toolchain GOROOT)", err)
	}
	fmt.Fprintf(&b, "\n## Go standard library and runtime\n\nLicense: BSD-3-Clause\n\n```text\n%s\n```\n", strings.TrimRight(string(goLicense), "\n"))
	var missing []string
	for _, c := range bom.Components {
		if c.Type != "library" {
			continue
		}
		license := "not reviewed"
		if len(c.Licenses) > 0 {
			license = c.Licenses[0].Expression
		}
		fmt.Fprintf(&b, "\n## %s %s\n\nLicense: %s\n\n", c.Name, c.Version, license)
		text, err := licenseText(modcache, c.Name, c.Version)
		if err != nil {
			missing = append(missing, c.Name+"@"+c.Version)
			continue
		}
		b.WriteString("```text\n")
		b.WriteString(strings.TrimRight(text, "\n"))
		b.WriteString("\n```\n")
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("no license text found in the module cache for: %s", strings.Join(missing, ", "))
	}
	return b.String(), nil
}

// licenseText reads a module's license file. Module cache paths escape
// upper-case letters as "!" + lower case.
func licenseText(modcache, path, version string) (string, error) {
	dir := filepath.Join(modcache, escape(path)+"@"+version)
	for _, name := range licenseFiles {
		if raw, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			return string(raw), nil
		}
	}
	// Otherwise any top-level LICENSE*/LICENCE*/COPYING* file (for example
	// LICENSE-APACHE-2.0.txt), in name order, all concatenated.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var texts []string
	for _, e := range entries {
		upper := strings.ToUpper(e.Name())
		if e.Type().IsRegular() && (strings.HasPrefix(upper, "LICENSE") || strings.HasPrefix(upper, "LICENCE") || strings.HasPrefix(upper, "COPYING")) {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return "", err
			}
			texts = append(texts, string(raw))
		}
	}
	if len(texts) == 0 {
		return "", fmt.Errorf("%s: no license file", dir)
	}
	return strings.Join(texts, "\n"), nil
}

func escape(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			b.WriteRune(r + ('a' - 'A'))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
