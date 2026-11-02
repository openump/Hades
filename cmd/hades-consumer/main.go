// hades-consumer is a thin wrapper; the command lives in internal/cmds/consumer and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/consumer"
)

func main() { os.Exit(consumer.Main(os.Args[1:])) }
