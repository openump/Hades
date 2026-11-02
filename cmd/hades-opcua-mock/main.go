//go:build opcua_mock

// hades-opcua-mock is a thin wrapper; the command lives in internal/cmds/opcuamock and is
// also reachable through the hades multicall binary.
package main

import (
	"os"

	"github.com/openump/Hades/internal/cmds/opcuamock"
)

func main() { os.Exit(opcuamock.Main(os.Args[1:])) }
