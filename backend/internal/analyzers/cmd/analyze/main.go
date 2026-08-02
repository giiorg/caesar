// Command analyze bundles caesar's house-law analyzers as a `go vet
// -vettool` plugin (unitchecker speaks exactly that protocol, and vet
// handles build tags for us). Not under backend/cmd: internal tooling, not
// a public entry point.
package main

import (
	"golang.org/x/tools/go/analysis/unitchecker"

	"github.com/giiorg/caesar/backend/internal/analyzers/envaccess"
	"github.com/giiorg/caesar/backend/internal/analyzers/stubfake"
	"github.com/giiorg/caesar/backend/internal/analyzers/txboundary"
)

func main() {
	unitchecker.Main(envaccess.Analyzer, stubfake.Analyzer, txboundary.Analyzer)
}
