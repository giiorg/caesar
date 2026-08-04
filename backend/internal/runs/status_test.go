package runs_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestRunStatusTransitions(t *testing.T) {
	allowed := []struct{ from, to runs.RunStatus }{
		{runs.RunPending, runs.RunRunning},
		{runs.RunPending, runs.RunSuperseded},
		{runs.RunPending, runs.RunDead},
		{runs.RunRunning, runs.RunWaiting},
		{runs.RunWaiting, runs.RunRunning},
		{runs.RunRunning, runs.RunSucceeded},
		{runs.RunRunning, runs.RunFailed},
		{runs.RunRunning, runs.RunDead},
		{runs.RunWaiting, runs.RunDead},
	}
	for _, tc := range allowed {
		if !tc.from.CanTransition(tc.to) {
			t.Errorf("%s -> %s should be allowed", tc.from, tc.to)
		}
	}
	denied := []struct{ from, to runs.RunStatus }{
		{runs.RunRunning, runs.RunPending},
		{runs.RunSucceeded, runs.RunRunning},
		{runs.RunDead, runs.RunRunning},
		{runs.RunSuperseded, runs.RunRunning},
		{runs.RunPending, runs.RunSucceeded},
		{runs.RunPending, runs.RunWaiting},
	}
	for _, tc := range denied {
		if tc.from.CanTransition(tc.to) {
			t.Errorf("%s -> %s must be denied", tc.from, tc.to)
		}
	}
}

func TestRunStatusTerminality(t *testing.T) {
	terminal := []runs.RunStatus{runs.RunSucceeded, runs.RunFailed, runs.RunSuperseded, runs.RunDead}
	live := []runs.RunStatus{runs.RunPending, runs.RunRunning, runs.RunWaiting}
	for _, s := range terminal {
		if !s.Terminal() {
			t.Errorf("%s should be terminal", s)
		}
	}
	for _, s := range live {
		if s.Terminal() {
			t.Errorf("%s should not be terminal", s)
		}
	}
}

func TestPhaseStatusTransitions(t *testing.T) {
	allowed := []struct{ from, to runs.PhaseStatus }{
		{runs.PhasePending, runs.PhaseRunning},
		{runs.PhasePending, runs.PhaseSuperseded},
		{runs.PhaseRunning, runs.PhaseSucceeded},
		{runs.PhaseRunning, runs.PhaseFailed},
		{runs.PhaseRunning, runs.PhaseDead},
		{runs.PhaseRunning, runs.PhaseReconciling},
		{runs.PhaseRunning, runs.PhaseSuperseded},
		{runs.PhaseReconciling, runs.PhaseSucceeded},
		{runs.PhaseReconciling, runs.PhaseFailed},
		{runs.PhaseReconciling, runs.PhaseDead},
		{runs.PhaseReconciling, runs.PhaseSuperseded},
	}
	for _, tc := range allowed {
		if !tc.from.CanTransition(tc.to) {
			t.Errorf("%s -> %s should be allowed", tc.from, tc.to)
		}
	}
	// The two structurally-forbidden moves the spec calls out by name.
	if runs.PhaseRunning.CanTransition(runs.PhasePending) {
		t.Error("running -> pending must be structurally impossible")
	}
	if runs.PhaseReconciling.CanTransition(runs.PhasePending) {
		t.Error("reconciling -> pending must be structurally impossible")
	}
}

func TestStatusValidity(t *testing.T) {
	if runs.RunStatus("bogus").Valid() || runs.PhaseStatus("bogus").Valid() {
		t.Error("unknown statuses must be invalid")
	}
	if !runs.RunWaiting.Valid() || !runs.PhaseReconciling.Valid() {
		t.Error("declared statuses must be valid")
	}
}
