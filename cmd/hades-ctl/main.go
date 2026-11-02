// hades-ctl is a thin wrapper; the command lives in internal/cmds/ctl and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/ctl"
)

func main() { os.Exit(ctl.Main(os.Args[1:])) }
