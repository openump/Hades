// hades-check verifies contracts or validates one payload without a broker.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/openump/Hades/internal/conformance"
	"github.com/openump/Hades/internal/contracts"
)

func run() error {
	root := flag.String("root", ".", "repository root containing schemas and fixtures")
	refresh := flag.Bool("refresh-baseline", false, "explicitly record reviewed contract/fixture hashes")
	kind := flag.String("kind", "telemetry", "payload kind for -file")
	file := flag.String("file", "", "validate this JSON payload instead of the fixture suite")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *refresh {
		if *file != "" {
			return fmt.Errorf("refresh and file validation are mutually exclusive")
		}
		return conformance.WriteBaseline(*root)
	}
	if *file == "" {
		return conformance.Run(*root, os.Stdout)
	}
	v, err := contracts.New(os.DirFS(filepath.Join(*root, "schemas")))
	if err != nil {
		return err
	}
	input, err := os.Open(*file)
	if err != nil {
		return err
	}
	defer input.Close()
	raw, err := io.ReadAll(io.LimitReader(input, contracts.MaxPayloadBytes+1))
	if err != nil {
		return err
	}
	if _, err := v.Validate(*kind, raw); err != nil {
		return err
	}
	fmt.Println("payload valid")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
