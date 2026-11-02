// hades-pki is a thin wrapper; the command lives in internal/cmds/pki and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/pki"
)

func main() { os.Exit(pki.Main(os.Args[1:])) }
