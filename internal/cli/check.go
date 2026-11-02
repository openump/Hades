package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/openump/Hades/internal/contracts"
	"github.com/openump/Hades/internal/sample"
	"github.com/openump/Hades/internal/supervisor"
)

// CheckSite validates a scaffolded site: the catalog metadata against the
// contracts and the asset registry, every service definition against its
// pinned binary and file digests, and the lab credential directory.
func CheckSite(dir string, stdout, stderr io.Writer) int {
	if err := checkSite(dir); err != nil {
		fmt.Fprintln(stderr, "hades check:", err)
		return 1
	}
	fmt.Fprintf(stdout, "site %s valid\n", dir)
	return 0
}

func checkSite(dir string) error {
	site, err := LoadSite(dir)
	if err != nil {
		return err
	}
	if _, ok := profiles[site.Profile]; !ok {
		return fmt.Errorf("unknown profile %q", site.Profile)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("site directory must be private (0700)")
	}
	metadata := filepath.Join(dir, "catalog", "metadata.json")
	if _, err := sample.LoadContext(site.Root, metadata); err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	v, err := contracts.New(os.DirFS(filepath.Join(site.Root, "schemas")))
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(metadata)
	if err != nil {
		return err
	}
	if _, err := v.Validate("metadata", raw); err != nil {
		return fmt.Errorf("catalog metadata: %w", err)
	}
	if len(site.Services) == 0 {
		return fmt.Errorf("no services")
	}
	for _, name := range site.Services {
		cfg, err := supervisor.Load(filepath.Join(dir, "services", name+".json"))
		if err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
		if err := supervisor.Validate(cfg); err != nil {
			return fmt.Errorf("service %s: %w", name, err)
		}
		if cfg.Binary != site.Binary {
			return fmt.Errorf("service %s runs %s, not the site binary %s", name, cfg.Binary, site.Binary)
		}
	}
	if site.Lab {
		for _, f := range []string{"ca.crt", "history-adapter.crt", "history-adapter.key"} {
			if _, err := os.Stat(filepath.Join(site.PKI, f)); err != nil {
				return fmt.Errorf("lab credentials: %w", err)
			}
		}
	}
	return nil
}
