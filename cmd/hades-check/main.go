// hades-check is a thin wrapper; the command lives in internal/cmds/check and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/check"
)

func main() { os.Exit(check.Main(os.Args[1:])) }
