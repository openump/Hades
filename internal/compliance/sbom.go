package compliance

import (
	"bytes"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// licenses records the reviewed license of each known module (docs/dependencies.md).
// A module absent from this map is reported with no license claim rather than
// a guessed one.
var licenses = map[string]string{
	"github.com/openump/Hades":                    "Apache-2.0",
	"github.com/cyberphone/json-canonicalization": "Apache-2.0",
	"github.com/eclipse/paho.golang":              "EPL-2.0",
	"github.com/gorilla/websocket":                "BSD-2-Clause",
	"github.com/santhosh-tekuri/jsonschema/v6":    "Apache-2.0",
	"golang.org/x/net":                            "BSD-3-Clause",
	"golang.org/x/text":                           "BSD-3-Clause",
	"golang.org/x/sys":                            "BSD-3-Clause",
	"golang.org/x/sync":                           "BSD-3-Clause",
	"golang.org/x/time":                           "BSD-3-Clause",
	"golang.org/x/oauth2":                         "BSD-3-Clause",
	"golang.org/x/crypto":                         "BSD-3-Clause",
	"github.com/awcullen/opcua":                   "MIT",
	"github.com/modelcontextprotocol/go-sdk":      "Apache-2.0 OR MIT",
	"github.com/google/jsonschema-go":             "MIT",
	"github.com/yosida95/uritemplate/v3":          "BSD-3-Clause",
	"github.com/segmentio/encoding":               "MIT",
	"github.com/segmentio/asm":                    "MIT",
	"github.com/warpstreamlabs/bento":             "MIT",
	"github.com/apache/plc4x/plc4go":              "Apache-2.0",
	"github.com/google/uuid":                      "BSD-3-Clause",
	"github.com/pkg/errors":                       "BSD-2-Clause",
	"github.com/rs/zerolog":                       "MIT",
	"github.com/fatih/color":                      "MIT",
	"github.com/mattn/go-isatty":                  "MIT",
	"github.com/mattn/go-colorable":               "MIT",
}

// Component is a CycloneDX 1.5 component.
type Component struct {
	Type     string    `json:"type"`
	BOMRef   string    `json:"bom-ref"`
	Name     string    `json:"name"`
	Version  string    `json:"version"`
	PURL     string    `json:"purl"`
	Hashes   []Hash    `json:"hashes,omitempty"`
	Licenses []License `json:"licenses,omitempty"`
}

type Hash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

type License struct {
	Expression string `json:"expression"`
}

// BOM is a CycloneDX 1.5 JSON document.
type BOM struct {
	Format       string       `json:"bomFormat"`
	SpecVersion  string       `json:"specVersion"`
	Version      int          `json:"version"`
	Metadata     BOMMetadata  `json:"metadata"`
	Components   []Component  `json:"components"`
	Dependencies []Dependency `json:"dependencies"`
}

type BOMMetadata struct {
	Component Component `json:"component"`
}

type Dependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn"`
}

// inventory accumulates the modules embedded in deployed binaries, so the
// SBOM lists exactly what is linked. The Go h1: dirhash (SHA-256) is recorded
// as each module's hash.
type inventory struct {
	components map[string]Component
	dependsOn  map[string]map[string]bool
}

func newInventory() *inventory {
	return &inventory{components: map[string]Component{}, dependsOn: map[string]map[string]bool{}}
}

func (inv *inventory) add(binary []byte) error {
	info, err := buildinfo.Read(bytes.NewReader(binary))
	if err != nil {
		return err
	}
	app := component("application", info.Path, info.Main.Version, "")
	inv.components[app.BOMRef] = app
	if inv.dependsOn[app.BOMRef] == nil {
		inv.dependsOn[app.BOMRef] = map[string]bool{}
	}
	for _, dep := range info.Deps {
		m := dep
		if m.Replace != nil {
			m = m.Replace
		}
		lib := component("library", m.Path, m.Version, m.Sum)
		inv.components[lib.BOMRef] = lib
		inv.dependsOn[app.BOMRef][lib.BOMRef] = true
	}
	return nil
}

// bom renders a deterministic CycloneDX 1.5 document.
func (inv *inventory) bom() *BOM {
	bom := &BOM{Format: "CycloneDX", SpecVersion: "1.5", Version: 1, Metadata: BOMMetadata{Component: component("application", "github.com/openump/Hades", "site", "")}, Components: []Component{}, Dependencies: []Dependency{}}
	for _, c := range inv.components {
		bom.Components = append(bom.Components, c)
	}
	sort.Slice(bom.Components, func(i, j int) bool { return bom.Components[i].BOMRef < bom.Components[j].BOMRef })
	for ref, set := range inv.dependsOn {
		on := make([]string, 0, len(set))
		for dep := range set {
			on = append(on, dep)
		}
		sort.Strings(on)
		bom.Dependencies = append(bom.Dependencies, Dependency{Ref: ref, DependsOn: on})
	}
	sort.Slice(bom.Dependencies, func(i, j int) bool { return bom.Dependencies[i].Ref < bom.Dependencies[j].Ref })
	return bom
}

func component(kind, path, version, sum string) Component {
	c := Component{Type: kind, Name: path, Version: version, PURL: fmt.Sprintf("pkg:golang/%s@%s", path, version), BOMRef: fmt.Sprintf("pkg:golang/%s@%s", path, version)}
	if h, ok := goSumHash(sum); ok {
		c.Hashes = []Hash{{Alg: "SHA-256", Content: h}}
	}
	// A main package (module/cmd/x) carries its module's license.
	for module, lic := range licenses {
		if path == module || strings.HasPrefix(path, module+"/") {
			c.Licenses = []License{{Expression: lic}}
		}
	}
	return c
}

// goSumHash converts a Go "h1:<base64 sha256>" module hash to hex.
func goSumHash(sum string) (string, bool) {
	encoded, ok := strings.CutPrefix(sum, "h1:")
	if !ok {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 {
		return "", false
	}
	return hex.EncodeToString(raw), true
}
