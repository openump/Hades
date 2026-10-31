// hades-check verifies contracts, validates one payload without a broker, or
// writes the CRA / IEC 62443-4-2 security-control evidence pack for a site.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openump/Hades/internal/compliance"
	"github.com/openump/Hades/internal/conformance"
	"github.com/openump/Hades/internal/contracts"
)

func run() error {
	root := flag.String("root", ".", "repository root containing schemas and fixtures")
	refresh := flag.Bool("refresh-baseline", false, "explicitly record reviewed contract/fixture hashes")
	kind := flag.String("kind", "telemetry", "payload kind for -file")
	file := flag.String("file", "", "validate this JSON payload instead of the fixture suite")
	craReport := flag.Bool("cra-report", false, "write the CRA / IEC 62443-4-2 evidence pack for a secure site")
	siteConfig := flag.String("site-config", "", "reviewed openump-site/1 manifest (relative to -root) for -cra-report")
	outDir := flag.String("out-dir", "", "output directory for -cra-report")
	binaries := flag.String("binaries", "artifacts/hades-history-proxy", "comma-separated deployed binaries (relative to -root) to digest and inventory")
	policy := flag.String("policy", "", "optional actuation policy to identify in the report")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *craReport {
		if *refresh || *file != "" {
			return fmt.Errorf("-cra-report excludes -refresh-baseline and -file")
		}
		return report(*root, *siteConfig, *outDir, *binaries, *policy)
	}
	if *refresh {
		if *file != "" {
			return fmt.Errorf("refresh and file validation are mutually exclusive")
		}
		return conformance.WriteBaseline(*root)
	}
	if *file == "" {
		return conformance.Run(*root, os.Stdout)
	}
	v, err := contracts.New(os.DirFS(filepath.Join(*root, "schemas")))
	if err != nil {
		return err
	}
	input, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer input.Close()
	raw, err := io.ReadAll(io.LimitReader(input, contracts.MaxPayloadBytes+1))
	if err != nil {
		return err
	}
	if _, err := v.Validate(*kind, raw); err != nil {
		return err
	}
	fmt.Println("payload valid")
	return nil
}

// report writes sbom.cdx.json, cra-attestation.json and
// cra-compliance-report.md, and fails if any mandatory control does not pass
// or any control fails.
func report(root, manifest, outDir, binaries, policy string) error {
	if manifest == "" || outDir == "" {
		return fmt.Errorf("-cra-report requires -site-config and -out-dir")
	}
	var bins []string
	for _, b := range strings.Split(binaries, ",") {
		if b = strings.TrimSpace(b); b != "" {
			bins = append(bins, b)
		}
	}
	s, err := compliance.FromManifest(root, manifest, bins, policy, time.Now().UTC().Truncate(time.Second))
	if err != nil {
		return err
	}
	a, bom := compliance.Evaluate(s)
	if err := compliance.Write(outDir, a, bom); err != nil {
		return err
	}
	var failed []string
	for _, c := range a.Controls {
		fmt.Printf("%-14s %-34s %s\n", c.Outcome, c.ID, c.Requirement)
		if c.Outcome == compliance.Fail || (c.Mandatory && c.Outcome != compliance.Pass) {
			failed = append(failed, c.ID)
		}
	}
	if !a.Passed {
		return fmt.Errorf("security controls not passed: %s; see %s", strings.Join(failed, ", "), filepath.Join(outDir, "cra-compliance-report.md"))
	}
	fmt.Println("all mandatory security controls passed (evidence, not certification)")
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
