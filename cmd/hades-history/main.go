// hades-history is a thin wrapper; the command lives in internal/cmds/history and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/history"
)

func main() { os.Exit(history.Main(os.Args[1:])) }
