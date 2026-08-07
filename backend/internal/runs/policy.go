package runs

import (
	"errors"
	"fmt"
	"math"
)

// snapshotSchemaVersion is the only snapshot shape this binary WRITES.
// Readers additionally accept version 1 (see snapshotFromRow), which is
// the Plan 05 shape without AllowSelfApproval.
const snapshotSchemaVersion = 2

// maxAttemptsCap bounds retry storms regardless of future baselines.
const maxAttemptsCap = 10

// PolicySnapshot is the minimal pinned policy a run carries (spec §2).
// It is evaluated once at intake and immutable thereafter; Plan 09
// replaces the evaluation, never the pinning.
type PolicySnapshot struct {
	SchemaVersion     int   `json:"schema"`
	MaxAttempts       int32 `json:"max_attempts"`
	ExecutionBudgetMS int64 `json:"execution_budget_ms"`
	WallClockMS       int64 `json:"wall_clock_ms"`
	// AllowSelfApproval lets the requester decide their own gate (spec §6).
	// True in the shipped baseline — teams flip it in the versioned baseline
	// Plan 09 introduces. Self-approvals are still marked distinctly in audit.
	AllowSelfApproval bool `json:"allow_self_approval"`
}

// DefaultSnapshot is the V1 shipped baseline (plan header pins).
func DefaultSnapshot() PolicySnapshot {
	return PolicySnapshot{
		SchemaVersion:     snapshotSchemaVersion,
		MaxAttempts:       3,
		ExecutionBudgetMS: 2 * 60 * 60 * 1000,
		WallClockMS:       24 * 60 * 60 * 1000,
		AllowSelfApproval: true,
	}
}

// Validate keeps checking for the CURRENT schema: what a caller hands
// insertRun must be the shape this binary writes. The tolerance for an older
// stored document lives on the read path only (snapshotFromRow).
func (s PolicySnapshot) Validate() error {
	var problems []error
	if s.SchemaVersion != snapshotSchemaVersion {
		problems = append(problems, fmt.Errorf("schema must be %d", snapshotSchemaVersion))
	}
	if s.MaxAttempts < 1 || s.MaxAttempts > maxAttemptsCap {
		problems = append(problems, fmt.Errorf("max_attempts must be 1-%d", maxAttemptsCap))
	}
	if s.ExecutionBudgetMS <= 0 {
		problems = append(problems, errors.New("execution_budget_ms must be positive"))
	}
	if s.WallClockMS <= 0 {
		problems = append(problems, errors.New("wall_clock_ms must be positive"))
	}
	return errors.Join(problems...)
}

// RemainingBudgetMS never goes negative: an overspent run reports zero.
func RemainingBudgetMS(budgetMS, usedMS int64) int64 {
	if usedMS >= budgetMS {
		return 0
	}
	return budgetMS - usedMS
}

// AddUsageMS accumulates observed execution time: negative deltas are
// clock skew, ignored; the sum saturates instead of wrapping.
func AddUsageMS(usedMS, deltaMS int64) int64 {
	if deltaMS <= 0 {
		return usedMS
	}
	if usedMS > math.MaxInt64-deltaMS {
		return math.MaxInt64
	}
	return usedMS + deltaMS
}
