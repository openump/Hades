// hades-service is a thin wrapper; the command lives in internal/cmds/service and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/service"
)

func main() { os.Exit(service.Main(os.Args[1:])) }
