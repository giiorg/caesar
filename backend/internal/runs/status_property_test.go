//go:build property

package runs_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

var allRunStatuses = []runs.RunStatus{
	runs.RunPending, runs.RunRunning, runs.RunWaiting,
	runs.RunSucceeded, runs.RunFailed, runs.RunSuperseded, runs.RunDead,
}

var allPhaseStatuses = []runs.PhaseStatus{
	runs.PhasePending, runs.PhaseRunning, runs.PhaseWaitingDecision, runs.PhaseReconciling,
	runs.PhaseSucceeded, runs.PhaseFailed, runs.PhaseDead, runs.PhaseSuperseded,
}

// revisionRetiresASuccess names the ONE declared exception to the sink rule
// (Plan 08 decision 2) as an exact (from, to) pair — not "succeeded is
// exempt", which would wave through any future edge out of succeeded.
// Readiness reads a node's newest attempt row, so a revision that rewinds a
// slice must retire even a row that succeeded in the prior generation —
// otherwise that row stays newest and readable as done forever.
func revisionRetiresASuccess(from, to runs.PhaseStatus) bool {
	return from == runs.PhaseSucceeded && to == runs.PhaseSuperseded
}

// Terminal states admit no transition at all, for both entities — save for
// the single revision edge named above. The property pins that edge from both
// sides: the assertion below fails if it ever disappears, and the sampled
// check fails if any OTHER terminal edge appears.
func TestPropertyTerminalStatesAreSinks(t *testing.T) {
	// Asserted, not merely tolerated. A predicate that only permits the pair
	// would keep passing if someone deleted the edge, and a revision would
	// silently stop retiring succeeded rows.
	if !runs.PhaseSucceeded.CanTransition(runs.PhaseSuperseded) {
		t.Fatal("succeeded -> superseded must be legal: it is the declared exception this property exempts")
	}
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.SampledFrom(allRunStatuses).Draw(t, "from")
		to := rapid.SampledFrom(allRunStatuses).Draw(t, "to")
		if from.Terminal() && from.CanTransition(to) {
			t.Fatalf("terminal run status %s transitions to %s", from, to)
		}
		pf := rapid.SampledFrom(allPhaseStatuses).Draw(t, "pf")
		pt := rapid.SampledFrom(allPhaseStatuses).Draw(t, "pt")
		if pf.Terminal() && pf.CanTransition(pt) && !revisionRetiresASuccess(pf, pt) {
			t.Fatalf("terminal phase status %s transitions to %s", pf, pt)
		}
	})
}

// No random walk through the declared transitions ever re-enters
// pending, and self-transitions never exist (CAS legality, spec §7).
func TestPropertyWalksNeverResurrectPending(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cur := runs.PhasePending
		steps := rapid.IntRange(1, 20).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			next := rapid.SampledFrom(allPhaseStatuses).Draw(t, "next")
			if !cur.CanTransition(next) {
				continue
			}
			if next == runs.PhasePending {
				t.Fatalf("%s -> pending is representable", cur)
			}
			if next == cur {
				t.Fatalf("self-transition %s -> %s", cur, next)
			}
			cur = next
		}
	})
}
