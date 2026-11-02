// hades-opcua-gateway is a thin wrapper; the command lives in internal/cmds/opcuagateway and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/opcuagateway"
)

func main() { os.Exit(opcuagateway.Main(os.Args[1:])) }
