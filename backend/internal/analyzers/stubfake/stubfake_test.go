package stubfake_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/analyzers/stubfake"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), stubfake.Analyzer, "a")
}
