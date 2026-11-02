// hades-inspector is a thin wrapper; the command lives in internal/cmds/inspector and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/inspector"
)

func main() { os.Exit(inspector.Main(os.Args[1:])) }
