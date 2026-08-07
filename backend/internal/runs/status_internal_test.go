// In-package: the gate transitions are asserted against phaseTransitions
// itself, because "nothing transitions INTO waiting_decision" is a claim
// about the whole table and no exported call can spell it.
package runs

import "testing"

func TestPhaseStatus_WaitingDecisionIsLiveAndDecidable(t *testing.T) {
	if !PhaseWaitingDecision.Valid() {
		t.Fatal("waiting_decision must be a valid phase status")
	}
	if PhaseWaitingDecision.Terminal() {
		t.Fatal("waiting_decision is live: a human has not decided yet")
	}
	for _, to := range []PhaseStatus{PhaseSucceeded, PhaseFailed, PhaseDead, PhaseSuperseded} {
		if !PhaseWaitingDecision.CanTransition(to) {
			t.Errorf("waiting_decision -> %s must be legal", to)
		}
	}
	// A gate is never claimed: no worker ever moves it to running, and
	// nothing reconciles a row that holds no lease.
	for _, to := range []PhaseStatus{PhaseRunning, PhaseReconciling, PhasePending} {
		if PhaseWaitingDecision.CanTransition(to) {
			t.Errorf("waiting_decision -> %s must be illegal", to)
		}
	}
	// Nothing transitions INTO waiting_decision either: the row is inserted
	// in that state by gate materialization.
	for from, tos := range phaseTransitions {
		for _, to := range tos {
			if to == PhaseWaitingDecision {
				t.Errorf("%s -> waiting_decision must be illegal; gates are inserted waiting", from)
			}
		}
	}
}

func TestPhaseStatus_ARevisionMaySupersedeASucceededAttempt(t *testing.T) {
	// Readiness is the node's NEWEST attempt row (decision 2). A revision
	// that rewinds a slice must therefore be able to retire a row that
	// SUCCEEDED in the prior generation — otherwise that row stays newest
	// and forever readable as done, and the slice's downstream nodes never
	// re-run. This is the one edge out of a terminal phase state.
	if !PhaseSucceeded.CanTransition(PhaseSuperseded) {
		t.Fatal("succeeded -> superseded must be legal; a revision retires the whole slice")
	}
	for _, to := range []PhaseStatus{PhasePending, PhaseRunning, PhaseReconciling, PhaseFailed, PhaseDead} {
		if PhaseSucceeded.CanTransition(to) {
			t.Errorf("succeeded -> %s must stay illegal; only a revision may retire a success", to)
		}
	}
}

func TestRunStatus_WaitingTerminalizesOnRejectAndCompletesOnApprove(t *testing.T) {
	for _, to := range []RunStatus{RunRunning, RunSucceeded, RunFailed, RunDead} {
		if !RunWaiting.CanTransition(to) {
			t.Errorf("waiting -> %s must be legal", to)
		}
	}
	if RunWaiting.CanTransition(RunSuperseded) {
		t.Error("waiting -> superseded must be illegal: supersede-before-work only touches pending runs")
	}
}
