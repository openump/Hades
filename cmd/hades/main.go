// hades is the multicall binary: every Hades role and the init/up bootstrap
// in one executable. See internal/cli.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args, os.Stdout, os.Stderr)) }
