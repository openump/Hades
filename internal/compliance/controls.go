// Package compliance produces technical security-control evidence for a Hades
// site: a CycloneDX SBOM and checks mapped to IEC 62443-4-2 component
// requirements. It is evidence for an assessment, not a conformity assessment
// or certification under the EU Cyber Resilience Act.
package compliance

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openump/Hades/internal/site"
)

// Outcome of one control.
type Outcome string

const (
	Pass        Outcome = "PASS"
	Fail        Outcome = "FAIL"
	NotAssessed Outcome = "NOT_ASSESSED" // cannot be verified from the inputs; never counted as PASS
)

// Control is one checked requirement with the evidence behind its outcome.
type Control struct {
	ID          string   `json:"id"`
	Requirement string   `json:"requirement"` // IEC 62443-4-2 reference
	Title       string   `json:"title"`
	Mandatory   bool     `json:"mandatory"`
	Outcome     Outcome  `json:"outcome"`
	Evidence    []string `json:"evidence"`
}

func control(id, req, title string, mandatory bool) *Control {
	return &Control{ID: id, Requirement: req, Title: title, Mandatory: mandatory, Outcome: Pass}
}

func (c *Control) fail(format string, args ...any) {
	c.Outcome = Fail
	c.Evidence = append(c.Evidence, "FAIL: "+fmt.Sprintf(format, args...))
}

func (c *Control) note(format string, args ...any) {
	c.Evidence = append(c.Evidence, fmt.Sprintf(format, args...))
}

// Site is the reviewed input: a secure-profile site bundle directory.
type Site struct {
	Dir          string
	MosquittoCfg string   // relative path of the broker config
	RolesJSON    string   // relative path of the dynamic-security roles
	Certificates []string // relative paths of PEM certificates to check
	Binaries     []string // relative paths of deployed binaries to digest
	Now          time.Time
	CA           string // the site trust anchor; leaves must chain to it alone
	// ControlRoles may publish commands or receipts; every other role must not.
	ControlRoles map[string]bool
	// AdminRoles may publish to the dynamic-security $CONTROL topic and so
	// reconfigure every ACL at runtime; every other role must not.
	AdminRoles map[string]bool
	Name       string // site identity for the report; defaults to the directory name
	Manifest   string // optional reviewed openump-site/1 manifest
	Policy     string // optional actuation policy

	loaded    bool
	brokerCfg string
	brokerErr error
}

// broker returns the broker config, read once per evaluation so every
// control judges the same contents.
func (s *Site) broker() (string, error) {
	if !s.loaded {
		raw, err := os.ReadFile(s.path(s.MosquittoCfg))
		s.brokerCfg, s.brokerErr, s.loaded = string(raw), err, true
	}
	return s.brokerCfg, s.brokerErr
}

// path resolves a site-relative path; absolute paths are kept.
func (s Site) path(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.Dir, p)
}

// listeners splits a Mosquitto config into its listener sections. Lines
// before the first listener are global.
func listeners(cfg string) (global string, sections []string) {
	var cur *strings.Builder
	var g strings.Builder
	for _, line := range strings.Split(cfg, "\n") {
		if f := strings.Fields(strings.SplitN(line, "#", 2)[0]); len(f) > 0 && f[0] == "listener" {
			sections = append(sections, "")
			cur = &strings.Builder{}
		}
		if cur == nil {
			g.WriteString(line + "\n")
			continue
		}
		cur.WriteString(line + "\n")
		sections[len(sections)-1] = cur.String()
	}
	return g.String(), sections
}

// TLSMinimum checks every broker listener is TLS 1.3 with required client
// certificates (CR 1.1, CR 3.1). TLS settings are per listener in
// Mosquitto, so one plaintext listener fails the control.
func TLSMinimum(s *Site) *Control {
	c := control("transport-tls13", "CR 1.1, CR 3.1", "TLS 1.3 minimum on every broker listener", true)
	cfg, err := s.broker()
	if err != nil {
		c.Outcome = NotAssessed
		c.note("broker config unreadable: %v", err)
		return c
	}
	_, sections := listeners(cfg)
	if len(sections) == 0 {
		c.fail("no listener declared; Mosquitto would open a default plaintext listener")
	}
	for _, sec := range sections {
		name := directive(sec, "listener")[0] // the port
		tls := directive(sec, "tls_version")
		switch {
		case len(directive(sec, "certfile")) == 0:
			c.fail("listener %s has no certfile: cleartext MQTT", name)
		case len(tls) == 0 || tls[len(tls)-1] != "tlsv1.3":
			c.fail("listener %s tls_version %q; TLS 1.3 is required", name, strings.Join(tls, ","))
		default:
			c.note("listener %s tls_version tlsv1.3", name)
		}
		if v := directive(sec, "require_certificate"); len(v) == 0 || v[len(v)-1] != "true" {
			c.fail("listener %s does not require client certificates", name)
		}
	}
	c.note("scope: the broker config; the Go services' TLS 1.3 floor is a property of the source build (internal/transport) and is not inferred from binary digests")
	return c
}

// NoAnonymousAccess checks the broker refuses unauthenticated clients and
// requires verified client certificates (CR 1.1, CR 1.2).
func NoAnonymousAccess(s *Site) *Control {
	c := control("identity-no-anonymous", "CR 1.1, CR 1.2", "No anonymous broker access; mutual TLS identities required", true)
	cfg, err := s.broker()
	if err != nil {
		c.Outcome = NotAssessed
		c.note("broker config unreadable: %v", err)
		return c
	}
	// Without per_listener_settings, authentication settings are global and
	// apply to every listener; with it, each listener would need checking.
	if v := directive(cfg, "per_listener_settings"); len(v) > 0 && v[len(v)-1] == "true" {
		c.fail("per_listener_settings true: per-listener authentication is not evaluated; the secure profile uses global settings")
	}
	if v := directive(cfg, "allow_anonymous"); len(v) == 0 || v[len(v)-1] != "false" {
		c.fail("allow_anonymous is %q; must be false", strings.Join(v, ","))
	}
	if v := directive(cfg, "require_certificate"); len(v) == 0 || v[len(v)-1] != "true" {
		c.fail("require_certificate is %q; client certificates must be required", strings.Join(v, ","))
	}
	if v := directive(cfg, "use_identity_as_username"); len(v) == 0 || v[len(v)-1] != "true" {
		c.fail("use_identity_as_username is %q; ACLs must bind to the certificate identity", strings.Join(v, ","))
	}
	if len(directive(cfg, "acl_file")) > 0 {
		c.fail("a static acl_file is loaded alongside dynamic security; the secure profile must use roles only")
	}
	if c.Outcome == Pass {
		c.note("allow_anonymous false, require_certificate true, use_identity_as_username true")
	}
	return c
}

// LeastPrivilege checks the dynamic-security role matrix (CR 2.1): default
// deny, no wildcard grants, and only control roles may publish commands or
// receipts.
func LeastPrivilege(s *Site) *Control {
	c := control("authz-least-privilege", "CR 2.1", "Least-privilege topic authorization; control plane isolated", true)
	raw, err := os.ReadFile(s.path(s.RolesJSON))
	if err != nil {
		c.Outcome = NotAssessed
		c.note("roles unreadable: %v", err)
		return c
	}
	var roles struct {
		Default map[string]bool `json:"defaultACLAccess"`
		Roles   []struct {
			Name string `json:"rolename"`
			ACLs []struct {
				Type  string `json:"acltype"`
				Topic string `json:"topic"`
				Allow bool   `json:"allow"`
			} `json:"acls"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(raw, &roles); err != nil {
		c.fail("roles are not valid JSON: %v", err)
		return c
	}
	for _, k := range []string{"publishClientSend", "publishClientReceive", "subscribe"} {
		if roles.Default[k] {
			c.fail("defaultACLAccess.%s is allow; default must be deny", k)
		}
	}
	for _, role := range roles.Roles {
		for _, acl := range role.ACLs {
			if !acl.Allow {
				continue
			}
			if broadWildcard(acl.Topic) {
				c.fail("role %s has wildcard grant %s %q", role.Name, acl.Type, acl.Topic)
			}
			if acl.Type != "publishClientSend" {
				continue
			}
			if strings.HasPrefix(acl.Topic, "$") || strings.HasPrefix(acl.Topic, "+") || strings.HasPrefix(acl.Topic, "#") {
				if !s.AdminRoles[role.Name] {
					c.fail("non-admin role %s may publish to the broker control topic %q and rewrite ACLs", role.Name, acl.Topic)
				} else {
					c.note("admin role %s may reconfigure roles via %q (management identity; keep its credential offline)", role.Name, acl.Topic)
				}
				continue
			}
			if (mayMatch(acl.Topic, "command") || mayMatch(acl.Topic, "command_receipt")) && !s.ControlRoles[role.Name] {
				c.fail("non-control role %s may publish to the control plane topic %q", role.Name, acl.Topic)
			}
		}
	}
	if c.Outcome == Pass {
		names := make([]string, 0, len(s.ControlRoles))
		for n := range s.ControlRoles {
			names = append(names, n)
		}
		sort.Strings(names)
		c.note("default deny; %d roles, no wildcard grants; only %s may publish control-plane topics", len(roles.Roles), strings.Join(names, ", "))
	}
	return c
}

// broadWildcard reports a multi-level wildcard anywhere, or a single-level
// wildcard before the last level. A final "+" selects one channel or
// parameter and is how the reference roles scope grants.
func broadWildcard(filter string) bool {
	levels := strings.Split(filter, "/")
	for i, l := range levels {
		if l == "#" || (l == "+" && i != len(levels)-1) {
			return true
		}
	}
	return false
}

// mayMatch reports whether an MQTT topic filter can match some OpenUMP
// control topic openump/v1/{enterprise}/{site}/{area}/{line}/{asset}/{kind}/{parameter}.
func mayMatch(filter, kind string) bool {
	template := []string{"openump", "v1", "", "", "", "", "", kind, ""} // "" = any value
	levels := strings.Split(filter, "/")
	for i, l := range levels {
		if l == "#" {
			return true
		}
		if i >= len(template) {
			return false
		}
		if l != "+" && template[i] != "" && l != template[i] {
			return false
		}
	}
	return len(levels) == len(template)
}

// CertificateValidity checks each certificate is inside its validity window
// and chains to the site CA (CR 1.2, CR 1.9).
func CertificateValidity(s *Site) *Control {
	c := control("identity-certificate-validity", "CR 1.2, CR 1.9", "Certificates valid and chained to the site CA", true)
	if len(s.Certificates) == 0 {
		c.Outcome = NotAssessed
		c.note("no certificates supplied")
		return c
	}
	roots, intermediates := x509.NewCertPool(), x509.NewCertPool()
	if s.CA == "" {
		c.fail("no site CA named; chains cannot be verified against a trust anchor")
	} else if ca, err := readCert(s.path(s.CA)); err != nil {
		c.fail("site CA %s: %v", s.CA, err)
	} else {
		roots.AddCert(ca)
	}
	var leaves []*x509.Certificate
	var names []string
	for _, rel := range s.Certificates {
		cert, err := readCert(s.path(rel))
		if err != nil {
			c.fail("%s: %v", rel, err)
			continue
		}
		if cert.IsCA {
			if rel != s.CA {
				intermediates.AddCert(cert) // never a trust anchor
			}
		} else {
			leaves = append(leaves, cert)
			names = append(names, rel)
		}
		switch {
		case s.Now.Before(cert.NotBefore):
			c.fail("%s (%s) not valid until %s", rel, cert.Subject.CommonName, cert.NotBefore.UTC().Format(time.RFC3339))
		case s.Now.After(cert.NotAfter):
			c.fail("%s (%s) expired %s", rel, cert.Subject.CommonName, cert.NotAfter.UTC().Format(time.RFC3339))
		default:
			c.note("%s (%s) valid until %s", rel, cert.Subject.CommonName, cert.NotAfter.UTC().Format(time.RFC3339))
		}
	}
	for i, leaf := range leaves {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: s.Now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			c.fail("%s does not chain to the site CA %s: %v", names[i], s.CA, err)
		}
	}
	return c
}

// ArtifactDigests records the SHA-256 of each deployed binary and builds the
// SBOM from the same bytes (CR 3.4). A binary without readable Go build
// information fails the control: its components cannot be inventoried.
func ArtifactDigests(s *Site) (*Control, map[string]string, *BOM) {
	c := control("integrity-artifact-digests", "CR 3.4", "Deployed binaries identified by SHA-256 and inventoried", true)
	digests := map[string]string{}
	inv := newInventory()
	if len(s.Binaries) == 0 {
		c.fail("no deployed binaries supplied; artifact integrity cannot be evidenced")
	}
	for _, rel := range s.Binaries {
		raw, err := os.ReadFile(s.path(rel))
		if err != nil {
			c.fail("%s: missing (%v)", rel, err)
			continue
		}
		sum := sha256.Sum256(raw)
		digests[rel] = hex.EncodeToString(sum[:])
		c.note("%s sha256:%s", rel, digests[rel])
		if err := inv.add(raw); err != nil {
			c.fail("%s: no Go build information for the SBOM (%v)", rel, err)
		}
	}
	return c, digests, inv.bom()
}

func readCert(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// directive returns the values of a Mosquitto config directive, in order.
func directive(cfg, name string) []string {
	var values []string
	for _, line := range strings.Split(cfg, "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) >= 2 && fields[0] == name {
			values = append(values, fields[1])
		}
	}
	return values
}

// ResourceLimits checks the broker bounds its queues, message sizes and
// clients and disables the remote dynamic-security control API (CR 7.1, CR 2.1).
func ResourceLimits(s *Site) *Control {
	c := control("resilience-resource-limits", "CR 7.1", "Bounded broker queues, message sizes and clients", true)
	cfg, err := s.broker()
	if err != nil {
		c.Outcome = NotAssessed
		c.note("broker config unreadable: %v", err)
		return c
	}
	for _, name := range []string{"max_queued_messages", "max_queued_bytes", "max_inflight_messages", "max_packet_size", "message_size_limit", "global_max_clients"} {
		v := directive(cfg, name)
		if len(v) == 0 || v[len(v)-1] == "0" {
			c.fail("%s is unset or unlimited", name)
		} else {
			c.note("%s %s", name, v[len(v)-1])
		}
	}
	if v := directive(cfg, "enable_control_api"); len(v) == 0 || v[len(v)-1] != "false" {
		c.fail("enable_control_api is %q; remote dynamic-security mutation must be off", strings.Join(v, ","))
	} else {
		c.note("enable_control_api false")
	}
	return c
}

// ConfigurationIntegrity verifies the reviewed site manifest against the
// release files and records its revision and digest (CR 3.4).
func ConfigurationIntegrity(s *Site) *Control {
	c := control("integrity-site-configuration", "CR 3.4", "Site configuration matches its reviewed revision", true)
	if s.Manifest == "" {
		c.fail("no reviewed site manifest supplied; configuration integrity cannot be evidenced")
		return c
	}
	raw, err := os.ReadFile(s.path(s.Manifest))
	if err != nil {
		c.fail("%s: %v", s.Manifest, err)
		return c
	}
	m, err := site.Decode(raw)
	if err != nil {
		c.fail("%s: %v", s.Manifest, err)
		return c
	}
	if m.Profile != "secure" {
		c.fail("profile %q is not the secure production profile", m.Profile)
	}
	if err := site.Validate(s.Dir, m); err != nil {
		c.fail("release does not match manifest: %v", err)
		return c
	}
	c.note("revision %s, %d reviewed files, manifest sha256:%s", m.Revision, len(m.Files), site.Digest(m))
	return c
}

// PolicySignature records the actuation policy identity. Hades policies are
// reviewed, schema-validated files pinned by revision; they carry no
// signature, so signature status is reported as not assessed, never PASS.
func PolicySignature(s *Site) *Control {
	c := control("integrity-actuation-policy", "CR 3.4", "Actuation policy identity and signature status", false)
	c.Outcome = NotAssessed
	if s.Policy == "" {
		c.note("no actuation policy supplied (site without closed-loop actuation)")
		return c
	}
	raw, err := os.ReadFile(s.path(s.Policy))
	if err != nil {
		c.fail("%s: %v", s.Policy, err)
		return c
	}
	var p struct {
		Revision int64 `json:"policy_revision"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Revision < 1 {
		c.fail("%s: no valid policy_revision", s.Policy)
		return c
	}
	sum := sha256.Sum256(raw)
	c.note("%s policy_revision %d sha256:%s", s.Policy, p.Revision, hex.EncodeToString(sum[:]))
	c.note("policies are unsigned; integrity rests on review and the pinned digest above")
	return c
}

// RuntimeGuardrails names the audit and resilience bounds that are set by
// service flags at runtime and cannot be read from the release bundle.
func RuntimeGuardrails(*Site) *Control {
	c := control("resilience-runtime-guardrails", "CR 2.8, CR 7.1", "WAL durability bounds and PLC scan-cycle rate limits", false)
	c.Outcome = NotAssessed
	c.note("set per service at runtime (normalizer/transfer WAL segment and disk-reserve flags, gateway poll and actuator rate limits); verify on the running node")
	return c
}

// FromManifest builds the evaluated site from a reviewed secure release: the
// broker config and roles at their release paths, every certificate in the
// manifest's credential directory, and the given deployed binaries.
func FromManifest(root, manifest string, binaries []string, policy string, now time.Time) (Site, error) {
	s := Site{Dir: root, Manifest: manifest, Binaries: binaries, Policy: policy, Now: now,
		MosquittoCfg: "config/broker/security/mosquitto.conf", RolesJSON: "config/broker/security/roles.json",
		ControlRoles: map[string]bool{"hades-actuator": true, "command-proposer": true, "hades-mcp": true},
		AdminRoles:   map[string]bool{"admin": true}}
	raw, err := os.ReadFile(s.path(manifest))
	if err != nil {
		return s, err
	}
	m, err := site.Decode(raw)
	if err != nil {
		return s, fmt.Errorf("%s: %w", manifest, err)
	}
	s.Name = m.Project + "/" + m.Revision
	if dir := m.Secrets["HADES_SECURITY_DIR"]; dir != "" {
		certs, _ := filepath.Glob(filepath.Join(dir, "*.crt"))
		sort.Strings(certs)
		s.Certificates = certs
		s.CA = filepath.Join(dir, "ca.crt")
	}
	return s, nil
}
