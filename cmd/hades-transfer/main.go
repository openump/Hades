// hades-transfer is a thin wrapper; the command lives in internal/cmds/transfer and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/transfer"
)

func main() { os.Exit(transfer.Main(os.Args[1:])) }
