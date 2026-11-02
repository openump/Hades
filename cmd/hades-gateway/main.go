// hades-gateway is a thin wrapper; the command lives in internal/cmds/gateway and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/gateway"
)

func main() { os.Exit(gateway.Main(os.Args[1:])) }
