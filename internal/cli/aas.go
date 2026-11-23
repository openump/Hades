package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openump/Hades/internal/aas"
	"github.com/openump/Hades/internal/contracts"
)

// Import compiles a brownfield tag CSV or an AAS JSON environment into
// reviewed OpenUMP metadata + semantic-mapping pairs (Hades-045).
func Import(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hades import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "directory containing schemas/")
	fromCSV := fs.String("from-csv", "", "brownfield tag table (one row per measurement)")
	fromAAS := fs.String("from-aas", "", "AAS V3.0 JSON environment (.aasx is not supported)")
	outDir := fs.String("out-dir", "", "directory for <enterprise>.<site>.<asset>.{metadata,semantic-mapping}.json")
	force := fs.Bool("force", false, "replace existing output files")
	revision := fs.Int64("revision", 0, "metadata_revision of the generated catalog (required)")
	published := fs.String("published-at", "", "published_at, RFC 3339 (default: now); fix it for reproducible output")
	gateway := fs.String("gateway-id", "", "acquisition gateway_id (required)")
	protocol := fs.String("protocol", "", "acquisition protocol (required)")
	statusInterval := fs.Int64("status-interval-ms", 10000, "acquisition expected_status_interval_ms")
	enterprise := fs.String("enterprise", "", "AAS import: ISA-95 enterprise")
	site := fs.String("site", "", "AAS import: site")
	area := fs.String("area", "", "AAS import: area")
	line := fs.String("line", "", "AAS import: line")
	asset := fs.String("asset", "", "AAS import: asset segment (default: from the shell idShort)")
	assetType := fs.String("asset-type", "", "AAS import: asset_type (default: from assetInformation.assetType)")
	interval := fs.Int64("expected-interval-ms", 0, "AAS import: interval for elements without an "+aas.QualifierInterval+" qualifier")
	submodels := fs.String("submodels", "", "AAS import: comma-separated submodel idShorts to read measurements from")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "hades import:", err); return 1 }
	if fs.NArg() != 0 || (*fromCSV == "") == (*fromAAS == "") || *outDir == "" {
		return fail(fmt.Errorf("give exactly one of -from-csv or -from-aas, and -out-dir"))
	}
	at := time.Now().UTC()
	if *published != "" {
		var err error
		if at, err = time.Parse(time.RFC3339Nano, *published); err != nil {
			return fail(fmt.Errorf("-published-at: %w", err))
		}
	}
	settings := aas.Settings{Revision: *revision, PublishedAt: at, GatewayID: *gateway, Protocol: *protocol, ExpectedStatusIntervalMS: *statusInterval}
	v, err := contracts.New(os.DirFS(filepath.Join(*root, "schemas")))
	if err != nil {
		return fail(err)
	}
	var catalogs []aas.Catalog
	var warnings []string
	if *fromCSV != "" {
		var aasOnly []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "enterprise", "site", "area", "line", "asset", "asset-type", "expected-interval-ms", "submodels":
				aasOnly = append(aasOnly, "-"+f.Name)
			}
		})
		if len(aasOnly) > 0 {
			return fail(fmt.Errorf("%s apply to -from-aas; a tag table carries them as columns", strings.Join(aasOnly, ", ")))
		}
		f, err := os.Open(*fromCSV)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		if catalogs, err = aas.ImportCSV(f, settings, v); err != nil {
			return fail(fmt.Errorf("%s:\n%w", *fromCSV, err))
		}
	} else {
		f, err := os.Open(*fromAAS)
		if err != nil {
			return fail(err)
		}
		defer f.Close()
		opts := aas.AASOptions{Settings: settings, Enterprise: *enterprise, Site: *site, Area: *area, Line: *line,
			Asset: *asset, AssetType: *assetType, ExpectedIntervalMS: *interval}
		for _, s := range strings.Split(*submodels, ",") {
			if s = strings.TrimSpace(s); s != "" {
				opts.Submodels = append(opts.Submodels, s)
			}
		}
		if catalogs, warnings, err = aas.ImportAAS(f, opts, v); err != nil {
			return fail(fmt.Errorf("%s:\n%w", *fromAAS, err))
		}
	}
	for _, w := range warnings {
		fmt.Fprintln(stderr, "hades import: warning:", w)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fail(err)
	}
	written, err := aas.WriteAll(*outDir, catalogs, *force)
	for _, p := range written {
		fmt.Fprintln(stdout, p)
	}
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "%d asset(s) compiled; review the files, then validate with hades check -kind semantic-mapping -file <mapping> -metadata <metadata>\n", len(catalogs))
	return 0
}

// AAS runs `hades aas export`: a verified catalog, with optional status and
// telemetry snapshots, projected into an AAS V3.0 JSON environment.
func AAS(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "export" {
		fmt.Fprintln(stderr, "usage: hades aas export -metadata <file> -mapping <file> [-status <file>] [-telemetry <file>] [-out <file>]")
		return 2
	}
	fs := flag.NewFlagSet("hades aas export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "directory containing schemas/")
	metadataPath := fs.String("metadata", "", "OpenUMP metadata snapshot (required)")
	mappingPath := fs.String("mapping", "", "semantic-mapping for that snapshot (required)")
	statusPath := fs.String("status", "", "optional status snapshot for OperationalData")
	telemetryPath := fs.String("telemetry", "", "optional telemetry snapshot for OperationalData")
	out := fs.String("out", "-", "output file, - for stdout")
	force := fs.Bool("force", false, "replace an existing output file")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "hades aas export:", err); return 1 }
	if fs.NArg() != 0 || *metadataPath == "" || *mappingPath == "" {
		return fail(fmt.Errorf("-metadata and -mapping are required"))
	}
	v, err := contracts.New(os.DirFS(filepath.Join(*root, "schemas")))
	if err != nil {
		return fail(err)
	}
	load := func(kind, path string, into any) (map[string]any, error) {
		raw, err := contracts.ReadFile(path)
		if err != nil {
			return nil, err
		}
		object, err := v.Validate(kind, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if into != nil {
			if err := json.Unmarshal(raw, into); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		return object, nil
	}
	var c aas.Catalog
	if _, err := load("metadata", *metadataPath, &c.Metadata); err != nil {
		return fail(err)
	}
	if _, err := load("semantic-mapping", *mappingPath, &c.Mapping); err != nil {
		return fail(err)
	}
	var snap aas.Snapshots
	if *statusPath != "" {
		if snap.Status, err = load("status", *statusPath, nil); err != nil {
			return fail(err)
		}
	}
	if *telemetryPath != "" {
		if snap.Telemetry, err = load("telemetry", *telemetryPath, nil); err != nil {
			return fail(err)
		}
	}
	env, err := aas.Export(c, snap, v)
	if err != nil {
		return fail(err)
	}
	raw, err := aas.Marshal(env)
	if err != nil {
		return fail(err)
	}
	if *out == "-" {
		_, err = stdout.Write(raw)
	} else {
		err = aas.WriteFile(*out, raw, *force)
	}
	if err != nil {
		return fail(err)
	}
	return 0
}
