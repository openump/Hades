// hades-capacity is a thin wrapper; the command lives in internal/cmds/capacity and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/capacity"
)

func main() { os.Exit(capacity.Main(os.Args[1:])) }
