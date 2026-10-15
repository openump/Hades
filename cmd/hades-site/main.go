// hades-site manages reviewed reference Compose releases; all arguments are explicit.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/openump/Hades/internal/site"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	action := flag.String("action", "validate", "prepare, validate, apply, status, rollback, backup, restore")
	root := flag.String("root", ".", "approved release source")
	manifestPath := flag.String("manifest", "site.json", "reviewed manifest file")
	state := flag.String("state", "runtime/site", "private manager state")
	revision := flag.String("revision", "", "new reviewed revision name")
	project := flag.String("project", "", "stable isolated Compose project")
	profile := flag.String("profile", "dev", "dev or secure reference stack")
	metadata := flag.String("metadata", "examples/site/metadata.json", "approved snapshot")
	security := flag.String("security-dir", "", "external private credential directory")
	hostState := flag.String("host-state", "{}", "JSON map of declared absolute adapter state directories")
	ports := flag.String("ports", "{}", "JSON published port references")
	digest := flag.String("digest", "", "existing release digest for rollback or trusted backup SHA256 for restore")
	archive := flag.String("archive", "", "private cold backup tar")
	target := flag.String("target", "", "new isolated restore directory")
	flag.Parse()
	ctx, cancel := site.Deadline()
	defer cancel()
	if *action == "restore" {
		return site.Restore(*archive, *target, *digest)
	}
	if *action == "prepare" {
		secrets := map[string]string{}
		if *security != "" {
			secrets["HADES_SECURITY_DIR"] = *security
		}
		var mapping map[string]string
		if err := json.Unmarshal([]byte(*ports), &mapping); err != nil {
			return err
		}
		manifest, err := site.Prepare(*root, *revision, *project, *profile, *metadata, secrets, mapping)
		if err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(*hostState), &manifest.HostState); err != nil {
			return err
		}
		if err = site.Validate(*root, manifest); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(manifest)
	}
	manager, err := site.Open(*state, nil)
	if err != nil {
		return err
	}
	defer manager.Close()
	switch *action {
	case "status":
		manifest, err := manager.Active()
		if err != nil {
			return err
		}
		if err = manager.Live(ctx, manifest); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"revision": manifest.Revision, "digest": site.Digest(manifest), "live": true, "scope": "reference_compose_services_only"})
	case "rollback":
		return manager.Rollback(ctx, *digest)
	case "backup":
		return manager.Backup(ctx, *archive)
	case "apply", "validate":
		absolute, err := filepath.Abs(*manifestPath)
		if err != nil {
			return err
		}
		raw, err := site.Read(filepath.Dir(absolute), filepath.Base(absolute), 65536)
		if err != nil {
			return err
		}
		manifest, err := site.Decode(raw)
		if err != nil {
			return err
		}
		if *action == "apply" {
			return manager.Apply(ctx, *root, manifest)
		}
		if err = site.Validate(*root, manifest); err != nil {
			return err
		}
		// Validate the frozen snapshot and rendered Compose without applying services.
		return manager.Check(ctx, *root, manifest)
	default:
		return fmt.Errorf("unknown action")
	}
}
