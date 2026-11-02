// Package cli is the hades multicall entrypoint. One binary serves every role:
// invoked as `hades <command>` or through a symlink named after a standalone
// binary (hades-normalizer -> hades), it runs exactly the code the standalone
// cmd/hades-* binary runs, with the same flags and exit codes.
package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openump/Hades/internal/cmds/actuator"
	"github.com/openump/Hades/internal/cmds/capacity"
	"github.com/openump/Hades/internal/cmds/check"
	"github.com/openump/Hades/internal/cmds/consumer"
	"github.com/openump/Hades/internal/cmds/ctl"
	"github.com/openump/Hades/internal/cmds/gateway"
	"github.com/openump/Hades/internal/cmds/history"
	"github.com/openump/Hades/internal/cmds/historyproxy"
	"github.com/openump/Hades/internal/cmds/infer"
	"github.com/openump/Hades/internal/cmds/inspector"
	"github.com/openump/Hades/internal/cmds/mcp"
	"github.com/openump/Hades/internal/cmds/normalizer"
	"github.com/openump/Hades/internal/cmds/opcuagateway"
	"github.com/openump/Hades/internal/cmds/pki"
	"github.com/openump/Hades/internal/cmds/service"
	"github.com/openump/Hades/internal/cmds/simulator"
	"github.com/openump/Hades/internal/cmds/site"
	"github.com/openump/Hades/internal/cmds/transfer"
)

// Version is stamped by scripts/package-release (-X); "dev" otherwise.
var Version = "dev"

// Roles maps each standalone binary name to its command. hades-opcua-mock is
// a test fixture behind a build tag and is deliberately not a role.
var Roles = map[string]func([]string) int{
	"hades-actuator":      actuator.Main,
	"hades-capacity":      capacity.Main,
	"hades-check":         check.Main,
	"hades-consumer":      consumer.Main,
	"hades-ctl":           ctl.Main,
	"hades-gateway":       gateway.Main,
	"hades-history":       history.Main,
	"hades-history-proxy": historyproxy.Main,
	"hades-infer":         infer.Main,
	"hades-inspector":     inspector.Main,
	"hades-mcp":           mcp.Main,
	"hades-normalizer":    normalizer.Main,
	"hades-opcua-gateway": opcuagateway.Main,
	"hades-pki":           pki.Main,
	"hades-service":       service.Main,
	"hades-simulator":     simulator.Main,
	"hades-site":          site.Main,
	"hades-transfer":      transfer.Main,
}

// Run dispatches one invocation and returns the exit code. argv[0] selects a
// role when it names one; otherwise argv[1] is a subcommand.
func Run(argv []string, stdout, stderr io.Writer) int {
	name := strings.TrimSuffix(filepath.Base(argv[0]), ".exe")
	if role, ok := Roles[name]; ok {
		return role(argv[1:])
	}
	if len(argv) < 2 || argv[1] == "help" || argv[1] == "-h" || argv[1] == "--help" {
		usage(stderr)
		if len(argv) < 2 {
			return 2
		}
		return 0
	}
	sub, args := argv[1], argv[2:]
	switch sub {
	case "init":
		return Init(args, stdout, stderr)
	case "up":
		return Up(args, stdout, stderr)
	case "status":
		return Status(args, stdout, stderr)
	case "check":
		// hades check -site-dir <dir> validates a scaffolded site; any other
		// flags run hades-check unchanged.
		if dir, ok := siteDirArg(args); ok {
			return CheckSite(dir, stdout, stderr)
		}
		return check.Main(args)
	case "release":
		return Release(args, stdout, stderr)
	case "top":
		return ctl.Main(append([]string{"top"}, args...))
	case "version", "--version":
		fmt.Fprintln(stdout, "hades", Version)
		return 0
	case "roles":
		for _, r := range RoleNames() {
			fmt.Fprintln(stdout, r)
		}
		return 0
	}
	if role, ok := Roles["hades-"+sub]; ok {
		return role(args)
	}
	fmt.Fprintf(stderr, "hades: unknown command %q\n\n", sub)
	usage(stderr)
	return 2
}

// RoleNames lists the standalone binary names, sorted.
func RoleNames() []string {
	names := make([]string, 0, len(Roles))
	for n := range Roles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: hades <command> [flags]

Bootstrap:
  init     scaffold a site directory and evaluation credentials
  up       start a scaffolded site in the foreground (Ctrl+C stops it)
  status   show the supervised services of a site
  top      read-only terminal dashboard (hades-ctl top)
  check    -site-dir <dir> validates a scaffolded site; otherwise hades-check
  release  keygen|pubkey|sign|sign-file|verify|notices|sbom|tarball

Every standalone binary is also a command, with the same flags:
`)
	for _, r := range RoleNames() {
		fmt.Fprintf(w, "  %s\n", strings.TrimPrefix(r, "hades-"))
	}
	fmt.Fprint(w, `
A symlink named after a standalone binary runs that role, for example
  ln -s hades /usr/local/bin/hades-normalizer
`)
}

// siteDirArg reports a -site-dir flag in any accepted spelling
// (-site-dir x, --site-dir x, -site-dir=x, --site-dir=x).
func siteDirArg(args []string) (string, bool) {
	for i, a := range args {
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "site-dir" {
			continue
		}
		if hasValue {
			return value, true
		}
		if i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}
