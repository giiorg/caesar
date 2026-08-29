// Command descriptorgen writes the generated §10.9 workflow descriptor. Run via
// `task contracts:descriptor`; CI diffs the committed file.
//
// It lives under internal/workflows rather than backend/cmd/ for the same
// reason openapigen lives under internal/contracts: internal/arch's
// CheckCmdOnlyApp holds top-level cmd packages to importing internal/app alone,
// and a generator's whole job is to import the package it describes.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/giiorg/caesar/backend/internal/workflows"
)

func main() {
	out := flag.String("out", "api/workflow-descriptor.json", "output path")
	flag.Parse()

	// Indent + trailing newline, matching openapigen: these bytes are diffed by
	// contracts:check, so the format is part of the contract.
	raw, err := json.MarshalIndent(workflows.BuildDescriptor(), "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil { //nolint:gosec // committed, world-readable generated artifact
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
