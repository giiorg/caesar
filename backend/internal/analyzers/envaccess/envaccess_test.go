package envaccess_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/analyzers/envaccess"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), envaccess.Analyzer, "a", "internal/platform/config")
}
