package stubfake_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/giiorg/caesar/backend/internal/analyzers/stubfake"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), stubfake.Analyzer, "a")
}
