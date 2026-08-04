package runs_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestDefaultSnapshotIsPinned(t *testing.T) {
	want := runs.PolicySnapshot{SchemaVersion: 1, MaxAttempts: 3, ExecutionBudgetMS: 2 * 60 * 60 * 1000, WallClockMS: 24 * 60 * 60 * 1000}
	if diff := cmp.Diff(want, runs.DefaultSnapshot()); diff != "" {
		t.Errorf("default snapshot mismatch (-want +got):\n%s", diff)
	}
	if err := runs.DefaultSnapshot().Validate(); err != nil {
		t.Errorf("default snapshot must validate: %v", err)
	}
}

func TestSnapshotJSONShape(t *testing.T) {
	raw, err := json.Marshal(runs.DefaultSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema":1,"max_attempts":3,"execution_budget_ms":7200000,"wall_clock_ms":86400000}`
	if string(raw) != want {
		t.Errorf("snapshot json = %s, want %s", raw, want)
	}
}

func TestSnapshotValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*runs.PolicySnapshot)
	}{
		{"zero attempts", func(s *runs.PolicySnapshot) { s.MaxAttempts = 0 }},
		{"attempts over cap", func(s *runs.PolicySnapshot) { s.MaxAttempts = 11 }},
		{"zero budget", func(s *runs.PolicySnapshot) { s.ExecutionBudgetMS = 0 }},
		{"zero wall clock", func(s *runs.PolicySnapshot) { s.WallClockMS = 0 }},
		{"wrong schema", func(s *runs.PolicySnapshot) { s.SchemaVersion = 2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := runs.DefaultSnapshot()
			tc.mut(&s)
			if s.Validate() == nil {
				t.Error("want validation error")
			}
		})
	}
}

func TestBudgetArithmetic(t *testing.T) {
	if got := runs.RemainingBudgetMS(100, 40); got != 60 {
		t.Errorf("remaining = %d, want 60", got)
	}
	if got := runs.RemainingBudgetMS(100, 140); got != 0 {
		t.Errorf("overspent remaining = %d, want 0", got)
	}
	if got := runs.AddUsageMS(50, -10); got != 50 {
		t.Errorf("negative delta must be ignored, got %d", got)
	}
	if got := runs.AddUsageMS(math.MaxInt64-5, 10); got != math.MaxInt64 {
		t.Errorf("usage must saturate, got %d", got)
	}
}
