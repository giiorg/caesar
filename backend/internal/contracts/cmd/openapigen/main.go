// Command openapigen writes the generated OpenAPI document. Run via
// `task contracts:openapi`; CI diffs the committed file.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/giiorg/caesar/backend/internal/contracts"
)

func main() {
	out := flag.String("out", "api/openapi.json", "output path")
	flag.Parse()
	raw, err := contracts.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, raw, 0o644); err != nil { //nolint:gosec // committed, world-readable generated artifact
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
