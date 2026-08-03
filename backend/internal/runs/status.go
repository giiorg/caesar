// Package runs is the execution-spine bounded context (spec §2): Tasks,
// Runs, PhaseRun-as-queue with immutable attempt rows, artifacts,
// commit-ordered run events, and the outbox. Postgres is the sole
// dispatch authority; everything the spine needs from peer contexts
// arrives through the ports in ports.go, wired only in app.
package runs

// RunStatus is a run's lifecycle state. Transitions live in
// runTransitions; nothing ever transitions into pending, and waiting is
// declared for Plan 08's gate decisions.
type RunStatus string

const (
	RunPending    RunStatus = "pending"
	RunRunning    RunStatus = "running"
	RunWaiting    RunStatus = "waiting"
	RunSucceeded  RunStatus = "succeeded"
	RunFailed     RunStatus = "failed"
	RunSuperseded RunStatus = "superseded"
	RunDead       RunStatus = "dead"
)

// runTransitions is the run transition table (plan header). Data, not
// code: CAS sites and the property suite both read it.
var runTransitions = map[RunStatus][]RunStatus{
	RunPending: {RunRunning, RunSuperseded, RunDead},
	RunRunning: {RunWaiting, RunSucceeded, RunFailed, RunDead},
	RunWaiting: {RunRunning, RunDead},
}

func (s RunStatus) Valid() bool {
	switch s {
	case RunPending, RunRunning, RunWaiting, RunSucceeded, RunFailed, RunSuperseded, RunDead:
		return true
	}
	return false
}

func (s RunStatus) Terminal() bool {
	return s == RunSucceeded || s == RunFailed || s == RunSuperseded || s == RunDead
}

func (s RunStatus) CanTransition(to RunStatus) bool { return transitionIn(runTransitions[s], to) }

// PhaseStatus is one attempt row's state. A failed row always has a
// successor attempt row inserted in the same transaction (plan decision
// 3); running -> pending is unrepresentable.
type PhaseStatus string

const (
	PhasePending     PhaseStatus = "pending"
	PhaseRunning     PhaseStatus = "running"
	PhaseReconciling PhaseStatus = "reconciling"
	PhaseSucceeded   PhaseStatus = "succeeded"
	PhaseFailed      PhaseStatus = "failed"
	PhaseDead        PhaseStatus = "dead"
	PhaseSuperseded  PhaseStatus = "superseded"
)

var phaseTransitions = map[PhaseStatus][]PhaseStatus{
	PhasePending:     {PhaseRunning, PhaseSuperseded},
	PhaseRunning:     {PhaseSucceeded, PhaseFailed, PhaseDead, PhaseReconciling, PhaseSuperseded},
	PhaseReconciling: {PhaseSucceeded, PhaseFailed, PhaseDead, PhaseSuperseded},
}

func (s PhaseStatus) Valid() bool {
	switch s {
	case PhasePending, PhaseRunning, PhaseReconciling, PhaseSucceeded, PhaseFailed, PhaseDead, PhaseSuperseded:
		return true
	}
	return false
}

func (s PhaseStatus) Terminal() bool {
	return s == PhaseSucceeded || s == PhaseFailed || s == PhaseDead || s == PhaseSuperseded
}

func (s PhaseStatus) CanTransition(to PhaseStatus) bool {
	return transitionIn(phaseTransitions[s], to)
}

func transitionIn[T comparable](list []T, to T) bool {
	for _, v := range list {
		if v == to {
			return true
		}
	}
	return false
}
