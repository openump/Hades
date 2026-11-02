package compliance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Disclaimer is printed on every report. The evidence supports an assessment;
// it is not one.
const Disclaimer = "Technical security-control evidence for a Hades site, mapped to IEC 62443-4-2 component requirements. It is not a conformity assessment, CE marking, or certification under Regulation (EU) 2024/2847 (Cyber Resilience Act), and does not replace a manufacturer's assessment or a notified body."

// Attestation is the machine-readable result.
type Attestation struct {
	Disclaimer  string            `json:"disclaimer"`
	EvaluatedAt time.Time         `json:"evaluated_at"`
	Site        string            `json:"site"`
	Passed      bool              `json:"passed"` // every mandatory control PASS and no control FAIL
	Controls    []*Control        `json:"controls"`
	Artifacts   map[string]string `json:"artifact_sha256"`
	SBOM        string            `json:"sbom"`
}

// Evaluate runs every control and returns the attestation with the SBOM of
// the deployed binaries. The site passes only if every mandatory
// control passes; a failed optional control also fails the site.
func Evaluate(site Site) (*Attestation, *BOM) {
	s := &site
	digests, artifacts, bom := ArtifactDigests(s)
	controls := []*Control{TLSMinimum(s), NoAnonymousAccess(s), CertificateValidity(s), LeastPrivilege(s), digests, ResourceLimits(s), ConfigurationIntegrity(s), PolicySignature(s), RuntimeGuardrails(s)}
	sort.Slice(controls, func(i, j int) bool { return controls[i].ID < controls[j].ID })
	name := s.Name
	if name == "" {
		name = filepath.Base(s.Dir)
	}
	a := &Attestation{Disclaimer: Disclaimer, EvaluatedAt: s.Now.UTC(), Site: name, Passed: true, Controls: controls, Artifacts: artifacts, SBOM: "sbom.cdx.json"}
	for _, c := range controls {
		if (c.Mandatory && c.Outcome != Pass) || c.Outcome == Fail {
			a.Passed = false
		}
	}
	return a, bom
}

// Write produces sbom.cdx.json, cra-attestation.json and
// cra-compliance-report.md in outDir. The same inputs always give identical
// files.
func Write(outDir string, a *Attestation, bom *BOM) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for name, v := range map[string]any{"sbom.cdx.json": bom, "cra-attestation.json": a} {
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, name), append(raw, '\n'), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(outDir, "cra-compliance-report.md"), []byte(Markdown(a, bom)), 0o644)
}

// Markdown renders the human-readable report.
func Markdown(a *Attestation, bom *BOM) string {
	var b strings.Builder
	verdict := "All mandatory controls passed and no control failed."
	if !a.Passed {
		verdict = "**Not passed: a mandatory control did not pass or a control failed.**"
	}
	fmt.Fprintf(&b, "# Hades security-control evidence: %s\n\n", a.Site)
	fmt.Fprintf(&b, "> %s\n\n", a.Disclaimer)
	fmt.Fprintf(&b, "Evaluated at %s. %s\n\n", a.EvaluatedAt.Format(time.RFC3339), verdict)
	b.WriteString("| Control | IEC 62443-4-2 | Mandatory | Outcome |\n|---|---|---|---|\n")
	for _, c := range a.Controls {
		fmt.Fprintf(&b, "| %s | %s | %t | %s |\n", c.Title, c.Requirement, c.Mandatory, c.Outcome)
	}
	b.WriteString("\n## Evidence\n")
	for _, c := range a.Controls {
		fmt.Fprintf(&b, "\n### %s (%s): %s\n\n", c.Title, c.ID, c.Outcome)
		for _, e := range c.Evidence {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	fmt.Fprintf(&b, "\n## Software bill of materials\n\n`%s` (CycloneDX %s) lists %d components linked into the deployed binaries.\n", a.SBOM, bom.SpecVersion, len(bom.Components))
	unlicensed := 0
	for _, c := range bom.Components {
		if len(c.Licenses) == 0 {
			unlicensed++
		}
	}
	if unlicensed > 0 {
		fmt.Fprintf(&b, "%d components have no reviewed license recorded and are listed without a license claim.\n", unlicensed)
	}
	return b.String()
}
