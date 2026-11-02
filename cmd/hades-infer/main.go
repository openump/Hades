// hades-infer is a thin wrapper; the command lives in internal/cmds/infer and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/infer"
)

func main() { os.Exit(infer.Main(os.Args[1:])) }
