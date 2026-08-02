package envaccess_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/giiorg/caesar/backend/internal/analyzers/envaccess"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), envaccess.Analyzer, "a", "internal/platform/config")
}
