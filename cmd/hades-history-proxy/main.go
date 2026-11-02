// hades-history-proxy is a thin wrapper; the command lives in internal/cmds/historyproxy and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/historyproxy"
)

func main() { os.Exit(historyproxy.Main(os.Args[1:])) }
