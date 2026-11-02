// hades-site is a thin wrapper; the command lives in internal/cmds/site and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/site"
)

func main() { os.Exit(site.Main(os.Args[1:])) }
