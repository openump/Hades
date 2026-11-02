// hades-simulator is a thin wrapper; the command lives in internal/cmds/simulator and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/simulator"
)

func main() { os.Exit(simulator.Main(os.Args[1:])) }
