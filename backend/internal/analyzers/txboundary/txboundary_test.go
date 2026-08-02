package txboundary_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/giiorg/caesar/backend/internal/analyzers/txboundary"
)

func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), txboundary.Analyzer, "internal/billing")
}
