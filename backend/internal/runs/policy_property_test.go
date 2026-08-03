//go:build property

package runs_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// Accumulating arbitrary attempt durations keeps the budget identity:
// remaining is never negative, never grows, and remaining+used == budget
// exactly until the budget is breached (spec §7 budget arithmetic).
func TestPropertyBudgetAccounting(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		budget := rapid.Int64Range(1, 1_000_000).Draw(t, "budget")
		used := int64(0)
		prevRemaining := runs.RemainingBudgetMS(budget, used)
		steps := rapid.IntRange(1, 50).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			delta := rapid.Int64Range(-100, 100_000).Draw(t, "delta")
			used = runs.AddUsageMS(used, delta)
			remaining := runs.RemainingBudgetMS(budget, used)
			if remaining < 0 {
				t.Fatalf("remaining went negative: %d", remaining)
			}
			if remaining > prevRemaining {
				t.Fatalf("remaining grew: %d -> %d", prevRemaining, remaining)
			}
			if used <= budget && remaining+used != budget {
				t.Fatalf("identity broken: %d + %d != %d", remaining, used, budget)
			}
			prevRemaining = remaining
		}
	})
}
