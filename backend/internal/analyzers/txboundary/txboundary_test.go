package txboundary_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/analyzers/txboundary"
	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), txboundary.Analyzer, "internal/billing")
}
