// hades-actuator is a thin wrapper; the command lives in internal/cmds/actuator and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/actuator"
)

func main() { os.Exit(actuator.Main(os.Args[1:])) }
