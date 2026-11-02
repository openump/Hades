// hades-normalizer is a thin wrapper; the command lives in internal/cmds/normalizer and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/normalizer"
)

func main() { os.Exit(normalizer.Main(os.Args[1:])) }
