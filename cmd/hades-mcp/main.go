// hades-mcp is a thin wrapper; the command lives in internal/cmds/mcp and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/mcp"
)

func main() { os.Exit(mcp.Main(os.Args[1:])) }
