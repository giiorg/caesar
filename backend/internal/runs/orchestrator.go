package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

type OrchestratorConfig struct {
	Workers           int
	PollInterval      time.Duration
	LeaseTTL          time.Duration
	HeartbeatInterval time.Duration
	OutboxRetention   time.Duration
}

// staleLeaseAfter is when a credential lease counts as abandoned: ten times
// the runner's heartbeat interval, so an attempt that is merely slow is never
// mistaken for one whose worker died. Staleness alone releases nothing — it
// only hands the lease to reconciliation (spec §2.1, §2.8).
const staleLeaseAfter = 5 * time.Minute

// Orchestrator drives the queue: one janitor goroutine (run startup,
// deadline sweep, lease sweep + reconcile, stale-credential-lease sweep,
// outbox retention) and N claim workers. It owns no state Postgres doesn't own
// first (spec §1).
type Orchestrator struct {
	svc       *Service
	exec      Executor
	admission Admission
	// credentials is exec's credential-settling half, resolved once here
	// rather than asserted per tick. nil means this deployment's executor
	// holds no credentials, and every path that would settle one is skipped.
	credentials CredentialReconciler
	leases      StaleLeaseSource
	cfg         OrchestratorConfig
	log         *slog.Logger
	workerTag   string
	lastPrune   time.Time
	// jitter spreads delayed retries so a shared cap window does not wake
	// every parked attempt at once; a seam so tests can pin the schedule.
	jitter func() float64

	planMu sync.Mutex
	plans  map[uuid.UUID]cachedPlan // run id -> compiled plan + context
}

type cachedPlan struct {
	plan       ExecPlan
	projectID  uuid.UUID
	taskInputs map[string]any
	baseSHA    string
	repo       RepoRef
	// The three a publication renders into its pull request. All immutable
	// for a run's lifetime — a task's title and requester and the run's
	// pinned definition hash cannot change — so caching them is honest.
	//
	// The requester's EMAIL is deliberately not among them: it lives on a
	// mutable users row, and it is hashed into the publication's commit sha.
	// A cached copy re-resolved after a restart could differ between two
	// attempts of the same publication and break the rebuild that recovery
	// adopts by. authorizePublish resolves it once and writes it to
	// publish_intents.author_email, which is the durable authority thereafter.
	taskTitle      string
	definitionHash string
	requesterID    uuid.UUID
	// generation is NOT cached: it changes when a gate opens a revision, and
	// a stale value would stamp the wrong marker on an artifact. It is read
	// per use through generationOf.
}

// generationOf reads the run's current generation under a row lock, and both
// halves of that are load-bearing.
//
// Not cached: a revision bumps the counter under a different transaction than
// the one that compiled the plan, so a value carried on cachedPlan would be
// stale exactly when it matters.
//
// Locked: an unlocked SELECT returns the READ COMMITTED snapshot taken at
// statement start, so a revision committing between the read and the writes it
// feeds would stamp generation N-1 on a generation-N run — and
// GetNodeArtifact's provenance-first ordering would then serve the stale
// artifact to the node downstream. FOR UPDATE holds the row for the rest of
// the transaction, which is what makes the value the caller stamps the value
// the caller commits against. The lock is not a new one: every caller updates
// this same run row later in the same transaction anyway (AddRunUsage,
// FinishRun); it is only taken before the first stamp instead of after.
func (o *Orchestrator) generationOf(ctx context.Context, q *store.Queries, runID uuid.UUID) (int32, error) {
	row, err := q.GetRunForUpdate(ctx, runID)
	if err != nil {
		return 0, fmt.Errorf("runs: read generation: %w", err)
	}
	return row.Generation, nil
}

// NewOrchestrator wires the queue driver. leases may be nil: a deployment
// whose executor holds no provider credentials has no stale-lease sweep to
// run, and the janitor skips it rather than pretending it swept.
func NewOrchestrator(svc *Service, exec Executor, admission Admission, leases StaleLeaseSource,
	cfg OrchestratorConfig, log *slog.Logger,
) *Orchestrator {
	if admission == nil {
		admission = AlwaysAdmit{}
	}
	credentials, _ := exec.(CredentialReconciler)
	return &Orchestrator{
		svc: svc, exec: exec, admission: admission, credentials: credentials, leases: leases,
		cfg: cfg, log: log,
		workerTag: uuid.NewString()[:8],
		jitter:    rand.Float64, //nolint:gosec // G404: spreading retry wakeups, not a security decision
		plans:     map[uuid.UUID]cachedPlan{},
	}
}

// Run blocks until ctx is done, then drains. Errors from individual
// ticks are logged, never fatal: the loop is the process's heartbeat.
func (o *Orchestrator) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		o.janitorLoop(ctx)
	}()
	for i := 0; i < o.cfg.Workers; i++ {
		wg.Add(1)
		workerID := fmt.Sprintf("%s-w%d", o.workerTag, i)
		go func() {
			defer wg.Done()
			o.workerLoop(ctx, workerID)
		}()
	}
	wg.Wait()
	return nil
}

func (o *Orchestrator) janitorLoop(ctx context.Context) {
	tick := time.NewTicker(o.cfg.PollInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := o.startPendingRuns(ctx); err != nil {
			o.log.Error("orchestrator: start pending runs", "error", err)
		}
		if err := o.materializeResumedRuns(ctx); err != nil {
			o.log.Error("orchestrator: materialize resumed runs", "error", err)
		}
		if err := o.sweepExpiredRuns(ctx); err != nil {
			o.log.Error("orchestrator: wall-deadline sweep", "error", err)
		}
		if err := o.sweepExpiredLeases(ctx); err != nil {
			o.log.Error("orchestrator: lease sweep", "error", err)
		}
		if err := o.sweepStuckReconciling(ctx); err != nil {
			o.log.Error("orchestrator: stuck-reconciling sweep", "error", err)
		}
		if err := o.sweepStaleCredentialLeases(ctx); err != nil {
			o.log.Error("orchestrator: stale credential lease sweep", "error", err)
		}
		if o.svc.clock.Now().Sub(o.lastPrune) > time.Hour {
			o.lastPrune = o.svc.clock.Now()
			cutoff := o.svc.clock.Now().Add(-o.cfg.OutboxRetention)
			if _, err := store.New(o.svc.pool).PruneOutbox(ctx, cutoff); err != nil {
				o.log.Error("orchestrator: outbox prune", "error", err)
			}
			o.prunePlanCache(ctx)
		}
	}
}

func (o *Orchestrator) workerLoop(ctx context.Context, workerID string) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := o.admission.AdmitClaim(ctx); err != nil {
			o.log.Warn("orchestrator: claims refused", "worker", workerID, "reason", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(o.cfg.PollInterval):
			}
			continue
		}
		c, ok, err := o.svc.claimNext(ctx, workerID, o.cfg.LeaseTTL.Seconds())
		if err != nil {
			o.log.Error("orchestrator: claim", "error", err)
			ok = false
		}
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-time.After(o.cfg.PollInterval):
			}
			continue
		}
		o.execute(ctx, workerID, c)
	}
}

// cachedPlanCount is the cache's size; eviction leaves no trace in
// Postgres, so integration tests observe it through here.
func (o *Orchestrator) cachedPlanCount() int {
	o.planMu.Lock()
	defer o.planMu.Unlock()
	return len(o.plans)
}

// dropPlan forgets a terminal run's compiled plan. Callers evict only
// after their transaction commits: eviction needs eventual truth, not
// precision — a premature drop costs one recompile, a missed one leaks.
func (o *Orchestrator) dropPlan(runID uuid.UUID) {
	o.planMu.Lock()
	delete(o.plans, runID)
	o.planMu.Unlock()
}

// prunePlanCache drops cache entries whose runs are terminal — covers
// runs terminalized by another process (plan 06 carry-forward: the
// cache previously grew for the process lifetime).
func (o *Orchestrator) prunePlanCache(ctx context.Context) {
	o.planMu.Lock()
	ids := make([]uuid.UUID, 0, len(o.plans))
	for id := range o.plans {
		ids = append(ids, id)
	}
	o.planMu.Unlock()
	if len(ids) == 0 {
		return
	}
	terminal, err := store.New(o.svc.pool).ListTerminalRunIDsAmong(ctx, ids)
	if err != nil {
		o.log.Error("orchestrator: prune plan cache", "error", err)
		return
	}
	for _, id := range terminal {
		o.dropPlan(id)
	}
}

// loadPlan compiles (and caches) the run's execution context.
func (o *Orchestrator) loadPlan(ctx context.Context, runID uuid.UUID) (cachedPlan, error) {
	o.planMu.Lock()
	if cp, ok := o.plans[runID]; ok {
		o.planMu.Unlock()
		return cp, nil
	}
	o.planMu.Unlock()

	q := store.New(o.svc.pool)
	runRow, err := q.GetRun(ctx, runID)
	if err != nil {
		return cachedPlan{}, fmt.Errorf("runs: load run: %w", err)
	}
	taskRow, err := q.GetTask(ctx, runRow.TaskID)
	if err != nil {
		return cachedPlan{}, fmt.Errorf("runs: load task: %w", err)
	}
	repo, err := o.svc.repos.Repo(ctx, taskRow.RepoID)
	if err != nil {
		return cachedPlan{}, err
	}
	def, err := o.svc.defs.DefinitionByVersionID(ctx, taskRow.WorkflowVersionID)
	if err != nil {
		return cachedPlan{}, err
	}
	plan, err := compilePinned(def)
	if err != nil {
		return cachedPlan{}, err
	}
	inputs, err := unmarshalMap(taskRow.Inputs)
	if err != nil {
		return cachedPlan{}, err
	}
	cp := cachedPlan{
		plan: plan, projectID: taskRow.ProjectID, taskInputs: inputs,
		baseSHA: runRow.BaseSha, repo: repo,
		taskTitle: taskRow.Title, definitionHash: runRow.DefinitionHash,
		requesterID: taskRow.RequesterID,
	}
	o.planMu.Lock()
	o.plans[runID] = cp
	o.planMu.Unlock()
	return cp, nil
}

// materializeNode inserts one node's next attempt row inside the caller's tx
// and returns the event announcing it. Gates are inserted 'waiting_decision'
// — the claim CTE predicates on 'pending', so this single branch is the whole
// reason a human's thinking time never burns a lease or the execution budget.
func (o *Orchestrator) materializeNode(ctx context.Context, tx pgx.Tx, cp cachedPlan,
	runID uuid.UUID, nodeID string, attempt, maxAttempts, generation int32,
) (Event, error) {
	ref := AttemptRef{RunID: runID, NodeID: nodeID, Attempt: attempt}
	payload := map[string]any{"node_id": nodeID, "attempt": attempt, "generation": generation}
	if cp.plan.Nodes[nodeID].Kind != KindGate {
		if _, err := o.svc.insertPhaseTx(ctx, tx, runID, nodeID, attempt, maxAttempts, 0, generation, nil); err != nil {
			return Event{}, err
		}
		return Event{
			RunID: runID, ProjectID: cp.projectID, Kind: EventPhaseMaterialized,
			CorrelationID: ref.String(), Payload: payload,
		}, nil
	}
	if _, err := store.New(tx).InsertGatePhase(ctx, store.InsertGatePhaseParams{
		RunID: runID, NodeID: nodeID, Attempt: attempt, MaxAttempts: maxAttempts,
		Generation: generation, CreatedAt: o.svc.clock.Now(),
	}); err != nil {
		return Event{}, fmt.Errorf("runs: insert gate %s/%s#%d: %w", runID, nodeID, attempt, err)
	}
	if rev := cp.plan.Nodes[nodeID].Revision; rev != nil {
		payload["revision_target"] = rev.Target
		payload["max_rounds"] = rev.MaxRounds
	}
	if pub, ok := cp.plan.PublishFor(nodeID); ok {
		// The attention item says what an approval will authorize. A gate
		// that authorizes nothing is a checkpoint; one that authorizes a
		// publish is a release decision, and the UI must not conflate them.
		payload["authorizes"] = pub
	}
	return Event{
		RunID: runID, ProjectID: cp.projectID, Kind: EventGateWaiting,
		CorrelationID: ref.String(), Payload: payload,
	}, nil
}

// startPendingRuns flips pending runs to running and materializes their
// roots — one tx per run; the CAS makes concurrent processes safe.
func (o *Orchestrator) startPendingRuns(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListPendingRunIDs(ctx, 16)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		cp, err := o.loadPlan(ctx, runID)
		if err != nil {
			o.log.Error("orchestrator: plan load", "run", runID, "error", err)
			continue
		}
		now := o.svc.clock.Now()
		err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
			n, err := store.New(tx).StartRun(ctx, store.StartRunParams{ID: runID, StartedAt: &now})
			if err != nil {
				return err
			}
			if n == 0 { // superseded or started elsewhere — not ours.
				return nil
			}
			evs := []Event{{
				RunID: runID, ProjectID: cp.projectID, Kind: EventRunStarted,
				CorrelationID: "caesar-run-" + runID.String(),
			}}
			// Attempt bounds come from the run's pinned snapshot.
			runRow, err := store.New(tx).GetRun(ctx, runID)
			if err != nil {
				return err
			}
			snap, err := snapshotFromRow(runRow.PolicySnapshot)
			if err != nil {
				return err
			}
			maxAttempts := snap.MaxAttempts
			for _, root := range cp.plan.Roots() {
				// The generation is literally 1: a run that has not started
				// cannot have revised.
				ev, err := o.materializeNode(ctx, tx, cp, runID, root, 1, maxAttempts, 1)
				if err != nil {
					return err
				}
				evs = append(evs, ev)
			}
			return appendEvents(ctx, tx, now, evs)
		})
		if err != nil {
			o.log.Error("orchestrator: start run", "run", runID, "error", err)
		}
	}
	return nil
}

// sweepExpiredRuns terminalizes wall-deadline breaches (spec §3: the
// predicate in every deciding statement is the mechanism; this sweep
// just makes the death visible).
//
// Every live phase's credential obligation is settled BEFORE the run dies
// (spec §2.8: sync receipt and lease release precede the terminal event). A
// run whose obligation could not be settled this tick is left alone: it stays
// matched by the expired-run predicate and is retried next tick. Terminalizing
// anyway would make the ordering violation durable — terminalizeRun supersedes
// every live phase at once and FinishRun has no deadline predicate to catch it
// later — where a run stuck here is visible in the log and self-healing.
func (o *Orchestrator) sweepExpiredRuns(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListExpiredRunIDs(ctx, 16)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		if !o.settleExpiredPhases(ctx, runID) {
			continue
		}
		// A publication that may have landed is an obligation, not a detail.
		// Terminalizing over one makes the orphan durable — the run is dead,
		// its phases superseded, and nothing reconciles a dead run — so an
		// unsettled intent leaves the run for the next tick exactly as an
		// unsettled lease does.
		if !o.settleRunPublications(ctx, runID) {
			continue
		}
		out := Outcome{Class: OutcomePermanent, Code: CodeWallDeadlineExceeded, Message: "wall clock expired"}
		if err := o.terminalizeRun(ctx, runID, RunDead, out, EventRunDead); err != nil {
			o.log.Error("orchestrator: expire run", "run", runID, "error", err)
		}
	}
	return nil
}

// settleExpiredPhases fences every still-running phase of an expired run and
// settles the credential obligation of each phase it now owns, reporting
// whether the run may be terminalized.
//
// FENCING IS UNCONDITIONAL, and that is the load-bearing part. It used to sit
// behind this function's `o.credentials == nil` short-circuit, which meant
// production — whose executor is not a CredentialReconciler, and whose publish
// nodes bypass the executor entirely — fenced nothing at all. Nothing else
// fences a past-deadline phase, so an expired run stayed 'running' forever:
// publication settlement's quiescence could never become true, and the run
// could never die. A run that cannot die is worse than the orphan settlement
// exists to prevent. Only the credential half is skipped now.
//
// Fencing and settling stay in ONE pass, phase by phase, rather than a fencing
// sweep followed by a settling sweep. The order is what makes a lost fencing
// CAS mean something: losing it to another process means that process is
// mid-reconciliation on that phase right now, and terminalizing on top of it
// makes the ordering violation durable. A pass that fenced everything up front
// would take custody of phases a concurrent sweeper was already settling.
//
// The cleanup-only entry is deliberate. Full reconciliation ADOPTS a completed
// attempt and can evaluate it as a success — and FinishRun carries no deadline
// predicate, so an exit-0 attempt reconciled after the wall deadline would mark
// the run succeeded and this sweep's terminalization would silently lose its
// CAS. Past the deadline the run's outcome is already decided; only the
// obligations it leaves behind are still owed.
//
// Rows this sweep already moved to 'reconciling' on an earlier tick are picked
// up again: without that, a reconciliation that failed once would leave a phase
// whose credential is never settled and a run that terminalizes over it.
func (o *Orchestrator) settleExpiredPhases(ctx context.Context, runID uuid.UUID) bool {
	q := store.New(o.svc.pool)
	phases, err := q.ListPhasesForRun(ctx, runID)
	if err != nil {
		o.log.Error("orchestrator: expired-run phases", "run", runID, "error", err)
		return false
	}
	settled := true
	for _, p := range phases {
		switch PhaseStatus(p.Status) {
		case PhaseRunning:
			owned, ok := o.fencePhase(ctx, q, p.ID)
			if !ok {
				settled = false
			}
			if !owned {
				continue
			}
		case PhaseReconciling:
			// Already fenced — by this sweep on an earlier tick, or by the
			// lease sweep. Either way the obligation may still be owed.
		default:
			// Terminal, never claimed, or waiting on a human. A gate in
			// particular ran no container and therefore holds no credential;
			// there is nothing here to settle.
			continue
		}
		if o.credentials == nil {
			continue // no executor-held credentials in this deployment
		}
		ref := AttemptRef{RunID: runID, NodeID: p.NodeID, Attempt: p.Attempt}
		if err := o.credentials.ReconcileCredential(ctx, ref); err != nil {
			o.log.Error("orchestrator: settle credential past deadline", "ref", ref.String(), "error", err)
			settled = false
		}
	}
	return settled
}

// fencePhase takes ONE still-running phase away from its worker: the worker
// loses the CAS it would need to commit anything, so no new effect can be
// issued and a settling sweep is not racing one.
//
// owned says this sweep fenced the phase and therefore owes its settlement; ok
// says the run may still be terminalized this tick. The three answers are
// distinct and the middle one is easy to lose: a phase that moved because it is
// OVER owes nothing (owned false, ok true), while a phase another sweeper moved
// to 'reconciling' is one it is settling right now, and terminalizing on top of
// it makes the ordering violation durable (owned false, ok false).
//
// A fenced row is left 'reconciling', which is deliberately NOT terminal — the
// obligation is still owed, it just has no owner that can act on it. Every
// settlement in this file therefore reads quiescence as "nothing still
// running", never as "no live row".
func (o *Orchestrator) fencePhase(ctx context.Context, q *store.Queries, phaseID uuid.UUID) (owned, ok bool) {
	// The phase lease may still be future-dated (the worker's own deadline exit
	// races this sweep), so the plain MarkPhaseReconciling cannot serve here:
	// this variant fences on the RUN's expired deadline.
	_, err := q.MarkPhaseReconcilingPastDeadline(ctx, phaseID)
	switch {
	case err == nil:
		return true, true
	case errorsIsNoRows(err):
		return false, o.phaseSettledElsewhere(ctx, q, phaseID)
	default:
		o.log.Error("orchestrator: fence past-deadline phase", "phase", phaseID, "error", err)
		return false, false
	}
}

// sweepStaleCredentialLeases hands abandoned credential leases to
// reconciliation (spec §2.8). Expiry never releases a lease by itself: the old
// container may still be alive and refreshing, so only a concluded
// reconciliation ends custody.
//
// A lease whose phase is still live is left alone — its own heartbeat and the
// lease/stuck sweeps own it, and taking custody underneath a running attempt
// would fence a worker that is doing exactly what it should.
func (o *Orchestrator) sweepStaleCredentialLeases(ctx context.Context) error {
	if o.leases == nil || o.credentials == nil {
		return nil
	}
	stale, err := o.leases.StaleCredentialLeases(ctx, staleLeaseAfter)
	if err != nil {
		return err
	}
	q := store.New(o.svc.pool)
	for _, l := range stale {
		ref, err := ParseAttemptRef(l.AttemptRef)
		if err != nil {
			// Not a ref this build wrote — an operator hold, a future
			// subsystem. Reconciling it would act on a phase that does not
			// exist, so it is left where it is.
			o.log.Warn("orchestrator: stale lease holder is not an attempt",
				"credential", l.CredentialID, "holder", l.AttemptRef)
			continue
		}
		phase, err := q.GetPhaseByRef(ctx, store.GetPhaseByRefParams{
			RunID: ref.RunID, NodeID: ref.NodeID, Attempt: ref.Attempt,
		})
		switch {
		case errorsIsNoRows(err):
			// No phase behind the lease at all: nothing will ever conclude it
			// except this sweep.
		case err != nil:
			o.log.Error("orchestrator: stale lease phase", "ref", ref.String(), "error", err)
			continue
		case phaseIsLive(PhaseStatus(phase.Status)):
			continue
		}
		if err := o.credentials.ReconcileCredential(ctx, ref); err != nil {
			o.log.Error("orchestrator: reconcile stale credential lease",
				"credential", l.CredentialID, "ref", ref.String(), "error", err)
		}
	}
	return nil
}

// phaseSettledElsewhere answers the only question the lost fencing CAS leaves
// open: did the phase move because it is over, or because another sweeper
// claimed it? Losing that CAS to a concurrent process' MarkPhaseReconciling
// means that process is mid-reconciliation right now — its receipt and release
// have not landed yet, and terminalizing on top of it makes exactly the
// ordering violation this sweep exists to prevent durable. Only a terminal row
// is proof that nobody is still holding an obligation.
//
// A read error is treated as "not proven", which costs a retry next tick.
func (o *Orchestrator) phaseSettledElsewhere(ctx context.Context, q *store.Queries, phaseID uuid.UUID) bool {
	row, err := q.GetPhase(ctx, phaseID)
	if err != nil {
		o.log.Error("orchestrator: re-read fenced phase", "phase", phaseID, "error", err)
		return false
	}
	return PhaseStatus(row.Status).Terminal()
}

// phaseIsLive reports whether the attempt's own machinery still owns it: a
// running phase heartbeats, a reconciling one is mid-recovery. Everything else
// — terminal, never claimed, or waiting on a human — has nobody left to
// conclude its credential. A gate is deliberately NOT live here: it never ran
// a container, so it never took a credential lease, and treating it as live
// would strand any lease whose ref collided with a gate node's.
func phaseIsLive(s PhaseStatus) bool {
	return s == PhaseRunning || s == PhaseReconciling
}

// terminalizeRun is the shared one-tx run death: CAS the run, supersede
// live phases, append events, audit run.finished (actor NULL).
// Lock order here is run -> phases, while evaluateSuccess goes phase ->
// run (finishPhase, then AddRunUsage); a wall-deadline sweep racing a
// worker's terminal evaluation of the same run can deadlock on that
// inversion. Postgres detects it and kills one side, and both recover —
// the sweeper on its next tick, the worker via lease expiry ->
// reconcile — so the inversion is documented rather than reordered.
func (o *Orchestrator) terminalizeRun(ctx context.Context, runID uuid.UUID, to RunStatus, out Outcome, eventKind string) error {
	cp, err := o.loadPlan(ctx, runID)
	if err != nil {
		return err
	}
	rawOut, err := json.Marshal(out)
	if err != nil {
		return err
	}
	now := o.svc.clock.Now()
	var finished int64
	err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).FinishRun(ctx, store.FinishRunParams{
			ID: runID, ToStatus: string(to), Outcome: rawOut, FinishedAt: &now,
		})
		if err != nil {
			return err
		}
		finished = n
		if n == 0 { // already terminal — lost the race, fine.
			return nil
		}
		superOut, _ := json.Marshal(Outcome{Class: OutcomePermanent, Code: CodeSuperseded, Message: "run terminalized"})
		if _, err := store.New(tx).SupersedeLivePhases(ctx, store.SupersedeLivePhasesParams{
			RunID: runID, Outcome: superOut, FinishedAt: &now,
		}); err != nil {
			return err
		}
		if err := appendEvents(ctx, tx, now, []Event{{
			RunID: runID, ProjectID: cp.projectID, Kind: eventKind,
			CorrelationID: "caesar-run-" + runID.String(), Payload: map[string]any{"outcome": out.Map()},
		}}); err != nil {
			return err
		}
		return o.svc.audit.Record(ctx, tx, audit.Entry{
			Action: "run.finished", SubjectKind: "run", SubjectID: runID.String(),
			Detail: map[string]any{"status": string(to), "outcome": out.Map()},
		})
	})
	if err == nil && finished > 0 {
		o.dropPlan(runID)
	}
	return err
}

// sweepExpiredLeases moves expired running attempts to reconciling and
// reconciles them immediately (spec §3: reconcile-before-retry; lease
// expiry NEVER auto-retries).
func (o *Orchestrator) sweepExpiredLeases(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListExpiredLeaseIDs(ctx, 16)
	if err != nil {
		return err
	}
	for _, phaseID := range ids {
		row, err := store.New(o.svc.pool).MarkPhaseReconciling(ctx, phaseID)
		if err != nil {
			if errorsIsNoRows(err) { // someone else got it — fine.
				continue
			}
			o.log.Error("orchestrator: mark reconciling", "phase", phaseID, "error", err)
			continue
		}
		o.reconcileAttempt(ctx, reconcileRow{
			PhaseID: row.ID, RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt,
			ChargeableFailures: row.ChargeableFailures, MaxAttempts: row.MaxAttempts,
			ClaimedAt: row.ClaimedAt, RunWallDeadline: row.RunWallDeadline,
		}, true)
	}
	return nil
}

// sweepStuckReconciling re-drives reconciling rows whose owner died
// between marking and evaluating (plan 06 carry-forward). The lease
// sweep cannot see them — its predicate is status = 'running' — so
// without this they sit until the wall deadline. The CAS lease-extension
// in ClaimStuckReconciling makes concurrent sweepers safe: one winner
// per row per grace window. Grace = 2x lease TTL.
func (o *Orchestrator) sweepStuckReconciling(ctx context.Context) error {
	rows, err := store.New(o.svc.pool).ClaimStuckReconciling(ctx, store.ClaimStuckReconcilingParams{
		LeaseSeconds: o.cfg.LeaseTTL.Seconds(),
		GraceSeconds: 2 * o.cfg.LeaseTTL.Seconds(),
		MaxRows:      16,
	})
	if err != nil {
		return err
	}
	for _, row := range rows {
		o.reconcileAttempt(ctx, reconcileRow{
			PhaseID: row.ID, RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt,
			ChargeableFailures: row.ChargeableFailures, MaxAttempts: row.MaxAttempts,
			ClaimedAt: row.ClaimedAt, RunWallDeadline: row.RunWallDeadline,
		}, false)
	}
	return nil
}

// reconcileRow is one attempt the reconcile path owns, in the shape both
// entry points share: the lease sweep and the stuck sweep read the same
// columns through different statements, and neither generated row type is
// the other's.
type reconcileRow struct {
	PhaseID            uuid.UUID
	RunID              uuid.UUID
	NodeID             string
	Attempt            int32
	ChargeableFailures int32
	MaxAttempts        int32
	ClaimedAt          *time.Time
	RunWallDeadline    time.Time
}

// reconcileAttempt runs reconcile-before-retry for one row already in
// 'reconciling' (spec §3): adopt a completed execution idempotently, adopt a
// completed FAILURE as the typed outcome the harness reported, and conclude
// anything genuinely unadoptable as transient reconcile_lost. announce writes
// the phase.reconciling event — true for the lease sweep, which is what put
// the row in the state, false for the stuck sweep, where the process
// that died already announced it.
func (o *Orchestrator) reconcileAttempt(ctx context.Context, row reconcileRow, announce bool) {
	cp, err := o.loadPlan(ctx, row.RunID)
	if err != nil {
		o.log.Error("orchestrator: reconcile plan load", "phase", row.PhaseID, "error", err)
		return
	}
	ref := AttemptRef{RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt}
	if announce {
		now := o.svc.clock.Now()
		if err := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
			return appendEvents(ctx, tx, now, []Event{{
				RunID: row.RunID, ProjectID: cp.projectID, Kind: EventPhaseReconciling,
				CorrelationID: ref.String(), Payload: map[string]any{"node_id": row.NodeID, "attempt": row.Attempt},
			}})
		}); err != nil {
			o.log.Error("orchestrator: reconciling event", "phase", row.PhaseID, "error", err)
		}
	}

	var ins InspectResult
	if cp.plan.Nodes[row.NodeID].Kind == KindGitPublish {
		// The Executor never saw this attempt: a publication is a control-plane
		// effect. Its durable position IS the intent row, and the forge is the
		// only witness for effects whose receipt was lost.
		//
		// It must not reach the Inspect path below, whose every unadoptable
		// answer is a CHARGEABLE reconcile_lost (outcome.go Chargeable): three
		// lost leases would exhaust a node whose branch is already on the forge
		// — terminalizing the run, superseding every phase, and leaving nothing
		// that will ever reconcile the branch. That is the exact failure
		// runPublish's invariant exists to forbid, entered by the one door
		// runPublish does not guard. Every answer inspectPublication gives is
		// either uncharged or the honest permanent one.
		ins, err = o.inspectPublication(ctx, ref)
	} else {
		ins, err = o.exec.Inspect(ctx, ref)
	}
	if err != nil || !ins.Completed {
		out := Outcome{Class: OutcomeTransient, Code: CodeReconcileLost, Message: "attempt lost its lease with no adoptable result"}
		o.evaluate(ctx, "", cp, claimedFromRow(row), PhaseReconciling, ExecutionResult{}, OutcomeError{Outcome: out})
		return
	}
	if ins.Outcome != nil {
		// The attempt finished and said why it failed. That answer is the
		// evaluation: a cap wait parks with its own reset, a dead credential
		// terminalizes naming itself — neither is a lost attempt, and
		// reconcile_lost would charge the node for both.
		o.evaluate(ctx, "", cp, claimedFromRow(row), PhaseReconciling, ins.Result, OutcomeError{Outcome: *ins.Outcome})
		return
	}
	// Completed with intact outputs: adopt idempotently.
	o.evaluate(ctx, "", cp, claimedFromRow(row), PhaseReconciling, ins.Result, nil)
}

func claimedFromRow(row reconcileRow) claimed {
	claimedAt := time.Time{}
	if row.ClaimedAt != nil {
		claimedAt = *row.ClaimedAt
	}
	return claimed{
		PhaseID: row.PhaseID,
		Ref:     AttemptRef{RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt},
		// The counter must travel every path that evaluates a failure, or
		// reconcile would restart the node's failure budget each time and
		// a lost attempt could never exhaust.
		ChargeableFailures: row.ChargeableFailures,
		MaxAttempts:        row.MaxAttempts,
		// The deadline travels for the adopted waits: a reconciled
		// rate_limited is a real uncharged retry, and it must park or
		// terminalize on exactly the boundary the worker path uses.
		RunWallDeadline: row.RunWallDeadline,
		ClaimedAt:       claimedAt,
	}
}

// execute runs one claimed attempt: deadline from the two clocks,
// heartbeats, port call, then evaluation.
func (o *Orchestrator) execute(ctx context.Context, workerID string, c claimed) {
	cp, err := o.loadPlan(ctx, c.Ref.RunID)
	if err != nil {
		// Never evaluate without a plan: a transient definition-source blip
		// would classify permanent and kill the run with project-less
		// events. Walk away instead — the lease expires and the reconcile
		// path retries honestly (transient reconcile_lost).
		o.log.Error("orchestrator: plan load", "run", c.Ref.RunID, "error", err)
		return
	}
	node := cp.plan.Nodes[c.Ref.NodeID]

	now := o.svc.clock.Now()
	if err := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseStarted,
			CorrelationID: c.Ref.String(),
			Payload:       map[string]any{"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt, "worker_id": workerID},
		}})
	}); err != nil {
		o.log.Error("orchestrator: started event", "error", err)
	}

	if node.Kind == KindGitPublish {
		// A publication is a control-plane effect with durable receipts, not
		// something a container produces. It shares the claim, the lease, the
		// heartbeat and the evaluation — only the middle differs.
		o.executePublish(ctx, workerID, cp, c)
		return
	}

	// Deadline = the tighter of the two clocks (spec §3 Budgets).
	deadline := c.RunWallDeadline
	if rem := RemainingBudgetMS(c.BudgetMS, c.UsedMS); rem > 0 {
		if budgetEnd := now.Add(time.Duration(rem) * time.Millisecond); budgetEnd.Before(deadline) {
			deadline = budgetEnd
		}
	}
	execCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	hbDone := o.startHeartbeat(execCtx, cancel, c, workerID)

	req, err := o.buildRequest(ctx, cp, node, c.Ref, deadline)
	var res ExecutionResult
	if err == nil {
		res, err = o.exec.Execute(execCtx, req)
	}
	close(hbDone)
	o.evaluate(ctx, workerID, cp, c, PhaseRunning, res, err)
}

// startHeartbeat keeps the lease alive until the returned channel closes or
// the attempt's context ends, and cancels the attempt the moment a heartbeat
// comes back fenced. Both execution paths share it so neither can forget to
// fence: a publication holds the same lease an agentic attempt does.
func (o *Orchestrator) startHeartbeat(execCtx context.Context, cancel context.CancelFunc,
	c claimed, workerID string,
) chan struct{} {
	hbDone := make(chan struct{})
	go func() {
		tick := time.NewTicker(o.cfg.HeartbeatInterval)
		defer tick.Stop()
		for {
			select {
			case <-hbDone:
				return
			case <-execCtx.Done():
				return
			case <-tick.C:
				alive, err := o.svc.heartbeat(execCtx, c.PhaseID, workerID, o.cfg.LeaseTTL.Seconds())
				if err == nil && !alive {
					cancel()
					return
				}
			}
		}
	}()
	return hbDone
}

// harnessBinding resolves the node's harness against the registry and with it
// the credential this attempt is pinned to for its whole life. A node that
// declares no harness binds nothing and that is not an error.
//
// It is a function of its own because it is the one part of request assembly
// that touches no run rows: the rest of buildRequest reads artifacts through
// the pool, so keeping this callable on its own is what lets the
// gone-vs-unreachable distinction below be pinned without a database.
func (o *Orchestrator) harnessBinding(ctx context.Context, node PlanNode) (uuid.UUID, CredentialBinding, error) {
	if node.Harness == "" {
		return uuid.Nil, CredentialBinding{}, nil
	}
	h, err := o.svc.harnesses.HarnessByName(ctx, node.Harness)
	// Only a harness that is genuinely GONE is permanent: retrying finds
	// the same nothing, so the node dies naming what an operator has to
	// re-register. Everything else here is the database being briefly
	// unreachable, and this resolution is two round trips wide — the row
	// and the credential kind it pins (app/runsports.go) — so a pool
	// timeout on either would otherwise kill the node permanently and tell
	// the operator something that is not true.
	switch {
	case err != nil && xerrors.ClassOf(err) == xerrors.ClassNotFound:
		return uuid.Nil, CredentialBinding{}, OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodeHarnessError,
			Message: fmt.Sprintf("harness %q no longer registered", node.Harness),
		}}
	case err != nil:
		return uuid.Nil, CredentialBinding{}, OutcomeError{Outcome: Outcome{
			Class: OutcomeTransient, Code: CodeInternalError,
			Message: fmt.Sprintf("resolve harness %q: %v", node.Harness, err),
		}}
	}
	return h.ID, CredentialBinding{ID: h.CredentialID, Kind: h.CredentialKind}, nil
}

// buildRequest assembles explicit inputs: latest artifacts of each input
// origin, the seeds a revision handed this node, and the harness binding.
func (o *Orchestrator) buildRequest(ctx context.Context, cp cachedPlan, node PlanNode, ref AttemptRef, deadline time.Time) (ExecutionRequest, error) {
	req := ExecutionRequest{
		Ref: ref, Kind: node.Kind, Repo: cp.repo, Instructions: node.Instructions,
		TaskInputs: cp.taskInputs, BaseSHA: cp.baseSHA, Deadline: deadline,
	}
	harnessID, binding, err := o.harnessBinding(ctx, node)
	if err != nil {
		return ExecutionRequest{}, err
	}
	req.HarnessID, req.Credential = harnessID, binding

	q := store.New(o.svc.pool)
	for _, in := range node.Inputs {
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
			RunID: ref.RunID, NodeID: in.Origin, Type: in.Artifact,
		})
		if err != nil {
			return ExecutionRequest{}, fmt.Errorf("runs: input artifact %s/%s: %w", in.Origin, in.Artifact, err)
		}
		payload, err := unmarshalMap(row.Payload)
		if err != nil {
			return ExecutionRequest{}, err
		}
		req.Inputs = append(req.Inputs, ArtifactInput{ID: row.ID, Type: row.Type, Payload: payload})
	}

	// Seeded inputs: what a revision handed this node. They are stored at the
	// node's OWN id with attempt 0, so a successor's structural lookup can
	// never mistake them for a produced artifact of an ancestor.
	seeds, err := q.ListSeedArtifacts(ctx, store.ListSeedArtifactsParams{
		RunID: ref.RunID, NodeID: ref.NodeID,
	})
	if err != nil {
		return ExecutionRequest{}, fmt.Errorf("runs: seed artifacts for %s: %w", ref.NodeID, err)
	}
	for _, row := range seeds {
		payload, err := unmarshalMap(row.Payload)
		if err != nil {
			return ExecutionRequest{}, err
		}
		req.Inputs = append(req.Inputs, ArtifactInput{ID: row.ID, Type: row.Type, Payload: payload})
	}

	// One patch reaches a phase, ever (Plan 07's cumulative-patch contract):
	// every runner-produced patch diffs from the run's base and subsumes its
	// ancestors, so a chain would double-apply. A seeded patch is strictly
	// newer than any structural one, so it displaces it. This has to be the
	// LAST statement: the helper keeps the last patch appended, which is the
	// seed only because the seed loop above has already run.
	req.Inputs = collapsePatchInputs(req.Inputs)
	return req, nil
}

// collapsePatchInputs keeps at most one patch input: the last one appended,
// which is the seed when a revision supplied one and the structural binding
// otherwise. Non-patch inputs pass through untouched and in order.
func collapsePatchInputs(in []ArtifactInput) []ArtifactInput {
	lastPatch := -1
	for i, a := range in {
		if a.Type == ArtifactKindPatch {
			lastPatch = i
		}
	}
	if lastPatch < 0 {
		return in
	}
	out := make([]ArtifactInput, 0, len(in))
	for i, a := range in {
		if a.Type == ArtifactKindPatch && i != lastPatch {
			continue
		}
		out = append(out, a)
	}
	return out
}

// evaluate is THE outcome transaction (spec §3 exhaustion never strands
// a node): success persists artifacts + materializes successors +
// completes the run; failure retries or terminalizes — each in one tx.
// fromStatus distinguishes worker completion from reconciler adoption.
func (o *Orchestrator) evaluate(ctx context.Context, workerID string, cp cachedPlan,
	c claimed, fromStatus PhaseStatus, res ExecutionResult, execErr error,
) {
	now := o.svc.clock.Now()
	deltaMS := now.Sub(c.ClaimedAt).Milliseconds()
	var runFinished bool
	err := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		var err error
		if execErr == nil {
			runFinished, err = o.evaluateSuccess(ctx, tx, workerID, cp, c, fromStatus, res, now, deltaMS)
		} else {
			runFinished, err = o.evaluateFailure(ctx, tx, workerID, cp, c, fromStatus, res, ClassifyOutcome(execErr), now, deltaMS)
		}
		return err
	})
	if err != nil {
		o.log.Error("orchestrator: evaluate", "ref", c.Ref.String(), "error", err)
		return
	}
	if runFinished {
		o.dropPlan(c.Ref.RunID)
	}
}

// evaluateSuccess reports whether this transaction terminalized the run,
// so the caller can evict its plan once the commit makes that true.
func (o *Orchestrator) evaluateSuccess(ctx context.Context, tx pgx.Tx, workerID string,
	cp cachedPlan, c claimed, fromStatus PhaseStatus, res ExecutionResult, now time.Time, deltaMS int64,
) (bool, error) {
	n, err := o.svc.finishPhase(ctx, tx, c.PhaseID, fromStatus, PhaseSucceeded, workerID, nil)
	if err != nil {
		return false, err
	}
	if n == 0 { // fenced: the row moved on without us. Drop the result.
		o.log.Info("orchestrator: fenced result dropped", "ref", c.Ref.String())
		return false, nil
	}
	q := store.New(tx)
	gen, err := o.generationOf(ctx, q, c.Ref.RunID)
	if err != nil {
		return false, err
	}

	// Persist artifacts with lineage = this node's input artifact ids.
	node := cp.plan.Nodes[c.Ref.NodeID]
	var parentIDs []string
	for _, in := range node.Inputs {
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{RunID: c.Ref.RunID, NodeID: in.Origin, Type: in.Artifact})
		if err == nil {
			parentIDs = append(parentIDs, row.ID.String())
		}
	}
	// A revised output descends from what the revision handed this node too:
	// the patch the gate examined and the note. Both were fed to the attempt,
	// and leaving them out would lose the only link between a revised artifact
	// and the round that asked for it.
	//
	// What parent_ids records, then, is DECLARED lineage plus seeds — not the
	// exact input set the attempt was handed. The two diverge on a revision
	// round: collapsePatchInputs drops the structurally-declared patch from the
	// request when a seeded one displaces it, yet the loop above still records
	// it. That is deliberate. The declared edge is a real ancestor of this
	// output whether or not its payload rode along in the request, and dropping
	// it here would be a lineage change with no caller asking for one.
	//
	// The statement's `generation = (SELECT generation FROM runs WHERE id=$1)`
	// subquery reads the same value this transaction is stamping the rows
	// with: generationOf above already holds the run row under GetRunForUpdate.
	seeds, err := q.ListSeedArtifacts(ctx, store.ListSeedArtifactsParams{
		RunID: c.Ref.RunID, NodeID: c.Ref.NodeID,
	})
	if err != nil {
		return false, err
	}
	for _, s := range seeds {
		parentIDs = append(parentIDs, s.ID.String())
	}
	if parentIDs == nil {
		parentIDs = []string{}
	}
	rawParents, err := json.Marshal(parentIDs)
	if err != nil {
		return false, err
	}
	artifactIDs, err := insertArtifacts(ctx, q, c.Ref, gen, res.Artifacts, rawParents, now)
	if err != nil {
		return false, err
	}

	usage, err := q.AddRunUsage(ctx, store.AddRunUsageParams{ID: c.Ref.RunID, DeltaMs: deltaMS})
	if err != nil {
		return false, err
	}

	evs := []Event{{
		RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseSucceeded, CorrelationID: c.Ref.String(),
		Payload: map[string]any{"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt, "artifact_ids": artifactIDs},
	}}

	// Budget breach terminalizes in this same transaction.
	if usage.ExecutionUsedMs >= usage.ExecutionBudgetMs {
		return true, o.finishRunTx(ctx, tx, cp, c.Ref.RunID, RunFailed,
			Outcome{Class: OutcomePermanent, Code: CodeBudgetExhausted, Message: "execution budget exhausted"},
			EventRunFailed, now, evs)
	}

	// Materialize newly-ready successors and read the run's resulting state
	// back. Both live in materializeReady, which the janitor's resumed-run
	// pass also calls: readiness has exactly one definition, so the two
	// callers cannot answer it differently.
	evs2, waiting, complete, err := o.materializeReady(ctx, tx, cp, c.Ref.RunID, c.MaxAttempts, gen, c.PhaseID)
	if err != nil {
		return false, err
	}
	evs = append(evs, evs2...)
	if complete {
		return true, o.finishRunTx(ctx, tx, cp, c.Ref.RunID, RunSucceeded, Outcome{}, EventRunSucceeded, now, evs)
	}
	if err := o.syncWaiting(ctx, tx, c.Ref.RunID, waiting); err != nil {
		return false, err
	}
	return false, appendEvents(ctx, tx, now, evs)
}

// materializeReady inserts a phase row for every node of the run whose inputs
// have all succeeded and which has no row spoken for it yet. It is the ONE
// definition of readiness: evaluateSuccess calls it after a phase lands, and
// the janitor calls it for runs a gate decision resumed (a decision is not a
// phase evaluation, so nothing else would ever pick those up).
//
// It returns the events announcing what it materialized, whether the run is
// now waiting on a human, and whether the run is complete.
func (o *Orchestrator) materializeReady(ctx context.Context, tx pgx.Tx, cp cachedPlan,
	runID uuid.UUID, maxAttempts, generation int32, excludePhase uuid.UUID,
) ([]Event, bool, bool, error) {
	q := store.New(tx)
	succeededNodes, err := q.ListSucceededNodes(ctx, runID)
	if err != nil {
		return nil, false, false, err
	}
	succeeded := map[string]bool{}
	for _, id := range succeededNodes {
		succeeded[id] = true
	}
	phaseRows, err := q.ListPhasesForRun(ctx, runID)
	if err != nil {
		return nil, false, false, err
	}
	live := map[string]bool{}
	// The next attempt ordinal per node. phase_runs is UNIQUE on
	// (run_id, node_id, attempt) (schema.sql:262), so a node a revision
	// rewound already owns ordinal 1 and materializing another one would be a
	// 23505, not a fresh attempt. The rows are already in hand, so this costs
	// nothing beyond the loop we are already running.
	nextAttempt := map[string]int32{}
	for _, pr := range phaseRows {
		if !PhaseStatus(pr.Status).Terminal() || succeeded[pr.NodeID] {
			live[pr.NodeID] = true
		}
		if pr.Attempt >= nextAttempt[pr.NodeID] {
			nextAttempt[pr.NodeID] = pr.Attempt + 1
		}
	}
	var evs []Event
	for _, nodeID := range cp.plan.Order {
		if live[nodeID] || !cp.plan.Ready(nodeID, succeeded) {
			continue
		}
		attempt := nextAttempt[nodeID]
		if attempt == 0 {
			attempt = 1 // never materialized before
		}
		ev, err := o.materializeNode(ctx, tx, cp, runID, nodeID, attempt, maxAttempts, generation)
		if err != nil {
			return nil, false, false, err
		}
		evs = append(evs, ev)
	}

	// The run's status is READ BACK from the database after the inserts, never
	// inferred from what this pass happened to do (baked decision 20).
	//
	// "Did I just insert a gate" is the wrong question twice over, and a
	// fan-out DAG asks it both ways. Insert a gate beside a runnable sibling
	// and it says waiting while the sibling is still pending — the run claims
	// to be waiting on a human who is not the blocker. Let a sibling finish
	// while a gate is ALREADY waiting and it says nothing, so the run stays
	// running — and because FinishGatePhase predicates on the run being
	// waiting, that gate is then undecidable until the wall deadline kills it.
	// Nothing in the validator forbids either shape: it checks form, cycles,
	// lineage, revisions and publication dominance, not "one branch at a time".
	//
	// `waiting` is DECLARED by this statement — there is no earlier
	// `waiting := false` to shadow, because a pre-insertion default is exactly
	// the inference decision 20 forbids.
	waiting, err := q.RunIsWaitingOnAGate(ctx, store.RunIsWaitingOnAGateParams{
		RunID: runID, ExcludePhase: excludePhase,
	})
	if err != nil {
		return nil, false, false, err
	}
	complete := len(succeeded) == len(cp.plan.Nodes)
	return evs, waiting.Bool, complete && !anyRunnablePhase(phaseRows, excludePhase), nil
}

// anyRunnablePhase reports whether the run still has work a worker could pick
// up. A run is complete only when the ONLY thing left is nothing at all: a
// parallel branch still running means the last succeeded node was not the last
// node, whatever the succeeded count says.
//
// exclude is the phase whose own completion is being evaluated, dropped for
// the same reason RunIsWaitingOnAGate takes an exclude_phase: the answer must
// not depend on when in the transaction the caller read these rows.
func anyRunnablePhase(rows []store.PhaseRun, exclude uuid.UUID) bool {
	for _, r := range rows {
		if r.ID == exclude {
			continue
		}
		switch PhaseStatus(r.Status) {
		case PhasePending, PhaseRunning, PhaseReconciling:
			return true
		}
	}
	return false
}

// syncWaiting moves the run between running and waiting to match what its
// phases actually say, so that both callers of materializeReady reconcile the
// run's status through one rule instead of each branching on the answer.
//
// The park direction is the live one: a sibling completing while a gate is
// ALREADY waiting must park the run, and that is the case which would
// otherwise strand the gate forever, because FinishGatePhase refuses to decide
// a run that is not waiting. Reading `waiting` back from the database rather
// than inferring it is what gets that case right — see materializeReady.
//
// The un-park direction is DEFENSIVE; neither call site reaches it today.
// materializeResumedRuns proves that by its own guard: it has already read the
// locked run row as 'running'. evaluateSuccess needs a longer argument, and
// "ClaimPhase issues a claim only while the run is 'running'" is not it — that
// covers the claim instant, not the interval up to here. What covers the
// interval is that nothing can have parked the run inside it:
// RunIsWaitingOnAGate never reports waiting while a pending, running or
// reconciling row of the run is visible to it other than its own caller's
// exclude_phase; this phase's row has been live since the claim, because
// liveness is only ever lost terminally (see phaseTransitions); it stays live
// to every other transaction until this one commits, since the finishPhase CAS
// above is still uncommitted; and no other transaction can be excluding it,
// because that same CAS admits exactly one evaluator of this row. So the run
// cannot read 'waiting' here. The CAS is written out rather than omitted so
// the rule stays symmetric: readiness answers "is this run waiting" and this
// pass writes that answer down, whichever way it came out. A zero row count in
// the un-park direction is the expected outcome, and in the park direction it
// means the run was already parked or was terminalized under us — success in
// both cases, never a conflict.
func (o *Orchestrator) syncWaiting(ctx context.Context, tx pgx.Tx, runID uuid.UUID, waiting bool) error {
	from, to := RunRunning, RunWaiting
	if !waiting {
		from, to = RunWaiting, RunRunning
	}
	_, err := store.New(tx).CASRunStatus(ctx, store.CASRunStatusParams{
		ID: runID, FromStatus: string(from), ToStatus: string(to),
	})
	return err
}

// materializeResumedRuns picks up runs a gate decision moved back to 'running'
// and materializes whatever the approval unblocked. Cheap: the query returns
// only running runs with nothing a worker could pick up, so a steady-state run
// matches nothing.
//
// "Nothing a worker could pick up" INCLUDES a run still holding another gate's
// waiting_decision row — see ListRunsAwaitingMaterialization. That is the
// parallel-gate case: this pass materializes what the decided gate released,
// and the syncWaiting below parks the run against the gate that is still open,
// which is what keeps that one decidable.
func (o *Orchestrator) materializeResumedRuns(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListRunsAwaitingMaterialization(ctx, 16)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		cp, err := o.loadPlan(ctx, runID)
		if err != nil {
			o.log.Error("orchestrator: resumed plan load", "run", runID, "error", err)
			continue
		}
		now := o.svc.clock.Now()
		var runFinished bool
		err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
			q := store.New(tx)
			// FOR UPDATE, and the generation it carries is the one every row
			// this pass stamps commits against (see generationOf). Taking it
			// here also serializes this pass against a worker's concurrent
			// evaluation of the same run.
			runRow, err := q.GetRunForUpdate(ctx, runID)
			if err != nil {
				return err
			}
			if RunStatus(runRow.Status) != RunRunning {
				return nil // terminalized or re-waiting under us; not ours.
			}
			snap, err := snapshotFromRow(runRow.PolicySnapshot)
			if err != nil {
				return err
			}
			evs, waiting, complete, err := o.materializeReady(ctx, tx, cp, runID,
				snap.MaxAttempts, runRow.Generation, uuid.Nil)
			if err != nil {
				return err
			}
			if complete {
				runFinished = true
				return o.finishRunTx(ctx, tx, cp, runID, RunSucceeded, Outcome{}, EventRunSucceeded, now, evs)
			}
			// Park the run if this pass left it waiting on a human. `waiting`
			// is read from the post-insertion state rather than from what this
			// pass did (see materializeReady).
			//
			// The un-park half of syncWaiting cannot fire here: the guard above
			// proved this run is 'running' under the row lock, and
			// ListRunsAwaitingMaterialization would not have offered a
			// 'waiting' run in the first place. It goes through syncWaiting
			// anyway so this pass and evaluateSuccess share one rule.
			if err := o.syncWaiting(ctx, tx, runID, waiting); err != nil {
				return err
			}
			return appendEvents(ctx, tx, now, evs)
		})
		if err != nil {
			o.log.Error("orchestrator: materialize resumed run", "run", runID, "error", err)
			continue
		}
		if runFinished {
			o.dropPlan(runID)
		}
	}
	return nil
}

// insertArtifacts persists one attempt's work products under the
// caller's tx and returns their ids for the event payload. Lineage is
// the caller's call: successes descend from their inputs, failures from
// nothing.
func insertArtifacts(ctx context.Context, q *store.Queries, ref AttemptRef, generation int32,
	artifacts []NewArtifact, rawParents []byte, now time.Time,
) ([]string, error) {
	var ids []string
	for _, a := range artifacts {
		rawPayload, err := json.Marshal(a.Payload)
		if err != nil {
			return nil, fmt.Errorf("runs: marshal artifact payload: %w", err)
		}
		id, err := uuid.NewV7()
		if err != nil {
			return nil, err
		}
		if _, err := q.InsertArtifact(ctx, store.InsertArtifactParams{
			ID: id, RunID: ref.RunID, NodeID: ref.NodeID, Attempt: ref.Attempt,
			Generation: generation,
			Type:       a.Type, Payload: rawPayload, ParentIds: rawParents, CreatedAt: now,
		}); err != nil {
			return nil, fmt.Errorf("runs: insert artifact: %w", err)
		}
		ids = append(ids, id.String())
	}
	return ids, nil
}

// finishRunTx completes/fails the run inside the caller's tx, appending
// the accumulated events first so ordering reads naturally.
//
// A free function over exactly the values it needs, because its two callers
// reach it from different layers — the janitor holds an Orchestrator and a
// cachedPlan, the gate decision runs in the API's goroutine and holds
// neither — and a run may have only one way to end.
func finishRunTx(ctx context.Context, tx pgx.Tx, rec *audit.Recorder,
	projectID, runID uuid.UUID, to RunStatus, out Outcome, eventKind string,
	now time.Time, evs []Event,
) error {
	var rawOut []byte
	payload := map[string]any{}
	if to != RunSucceeded {
		var err error
		if rawOut, err = json.Marshal(out); err != nil {
			return err
		}
		payload["outcome"] = out.Map()
	}
	n, err := store.New(tx).FinishRun(ctx, store.FinishRunParams{
		ID: runID, ToStatus: string(to), Outcome: rawOut, FinishedAt: &now,
	})
	if err != nil {
		return err
	}
	if n > 0 && to != RunSucceeded {
		superOut, _ := json.Marshal(Outcome{Class: OutcomePermanent, Code: CodeSuperseded, Message: "run terminalized"})
		if _, err := store.New(tx).SupersedeLivePhases(ctx, store.SupersedeLivePhasesParams{
			RunID: runID, Outcome: superOut, FinishedAt: &now,
		}); err != nil {
			return err
		}
	}
	evs = append(evs, Event{
		RunID: runID, ProjectID: projectID, Kind: eventKind,
		CorrelationID: "caesar-run-" + runID.String(), Payload: payload,
	})
	if err := appendEvents(ctx, tx, now, evs); err != nil {
		return err
	}
	detail := map[string]any{"status": string(to)}
	if to != RunSucceeded {
		detail["outcome"] = out.Map()
	}
	return rec.Record(ctx, tx, audit.Entry{
		Action: "run.finished", SubjectKind: "run", SubjectID: runID.String(), Detail: detail,
	})
}

// finishRunTx keeps the orchestrator's method form, now a forwarding shim, so
// its call sites are untouched.
func (o *Orchestrator) finishRunTx(ctx context.Context, tx pgx.Tx, cp cachedPlan, runID uuid.UUID,
	to RunStatus, out Outcome, eventKind string, now time.Time, evs []Event,
) error {
	return finishRunTx(ctx, tx, o.svc.audit, cp.projectID, runID, to, out, eventKind, now, evs)
}

// evaluateFailure terminalizes or retries a failed attempt, reporting
// whether it terminalized the run (see evaluateSuccess). An executor
// may return artifacts alongside its error (logs, failure snapshots):
// those are persisted once the fencing CAS proves this evaluation is the
// one that counts — parentless, because a failure descends from nothing.
func (o *Orchestrator) evaluateFailure(ctx context.Context, tx pgx.Tx, workerID string,
	cp cachedPlan, c claimed, fromStatus PhaseStatus, res ExecutionResult, out Outcome, now time.Time, deltaMS int64,
) (bool, error) {
	rawOut, err := json.Marshal(out)
	if err != nil {
		return false, err
	}
	noParents := []byte("[]") // a failure artifact descends from nothing
	// Usage accrues only after winning the fencing CAS below: a fenced
	// evaluation must leave no trace, or the reconciler and the hung
	// worker would each account the same claim->terminal interval
	// (invariant 11: usage accumulates once).
	q := store.New(tx)
	// A wait is not a failure: only chargeable outcomes spend the node's
	// budget, so the bound is the counter — never the attempt ordinal,
	// which outruns max_attempts across parked retries (spec §4.3).
	newCount := c.ChargeableFailures
	if out.Chargeable() {
		newCount++
	}
	retry := out.Retryable() && newCount < c.MaxAttempts
	// One locked read of the run row serves both branches below. It is
	// unconditional because the generation it carries is stamped on every row
	// this function writes — the retry's successor and artifacts, the dead
	// path's artifacts alike — and a generation read outside the lock is one a
	// concurrent revision can invalidate before this transaction commits.
	runRow, err := q.GetRunForUpdate(ctx, c.Ref.RunID)
	if err != nil {
		return false, err
	}
	gen := runRow.Generation
	budgetBreached := false
	if retry && !exemptFromBudgetPreCheck(out.Code) {
		// The fencing CAS must pick failed vs dead up front (failed ->
		// dead is not a legal transition), and a delta that crosses the
		// budget must not insert a successor the claim predicate
		// (execution_used_ms < execution_budget_ms) would refuse forever.
		// The run-row lock makes the read race-free; usage itself is
		// still only added after the CAS wins. A rate_limited attempt did
		// run before the window shut, so it keeps the pre-check.
		if runRow.ExecutionUsedMs+max(deltaMS, 0) >= runRow.ExecutionBudgetMs {
			retry, budgetBreached = false, true
		}
	}

	// A free requeue parks rather than running now — waiting is the whole
	// point (spec §4.4). Both the delay's origin and the deadline it is
	// checked against are database timestamps, so app-clock skew cannot
	// move the park-vs-terminalize boundary.
	var notBefore *time.Time
	parkedPastDeadline := false
	if retry && !out.Chargeable() {
		dbNow, err := q.DBNow(ctx)
		if err != nil {
			return false, err
		}
		nb := dbNow.Add(retryDelay(out, dbNow, c.Ref.Attempt-1-c.ChargeableFailures, o.jitter))
		// Both paths carry the deadline: an adopted cap wait is as real a
		// wait as the worker's own, and it parks or terminalizes on the same
		// boundary. A zero deadline means nobody read one, which is not a
		// licence to park past it — it only skips a comparison with nothing.
		if !c.RunWallDeadline.IsZero() && nb.After(c.RunWallDeadline) &&
			!exemptFromParkPastDeadline(out.Code) {
			// Never park a row that could not legally run when it woke —
			// except where terminalizing is the worse answer, because the
			// outcome leaves an effect behind that only the expiry sweep can
			// settle and a failed run is swept by nothing (outcome.go).
			retry, parkedPastDeadline = false, true
		} else {
			notBefore = &nb
		}
	}
	if retry {
		// Retry: prior attempt -> failed, next attempt inserted, SAME tx
		// (spec §2). failed rows always have successors (plan decision 3).
		n, err := o.svc.finishPhase(ctx, tx, c.PhaseID, fromStatus, PhaseFailed, workerID, rawOut)
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, nil // fenced
		}
		artifactIDs, err := insertArtifacts(ctx, q, c.Ref, gen, res.Artifacts, noParents, now)
		if err != nil {
			return false, err
		}
		// credential_busy never touched the harness — charging its
		// contention window would bill the run for someone else's lease.
		if deltaMS > 0 && out.Code != CodeCredentialBusy {
			if _, err := q.AddRunUsage(ctx, store.AddRunUsageParams{ID: c.Ref.RunID, DeltaMs: deltaMS}); err != nil {
				return false, err
			}
		}
		if _, err := o.svc.insertPhaseTx(ctx, tx, c.Ref.RunID, c.Ref.NodeID,
			c.Ref.Attempt+1, c.MaxAttempts, newCount, gen, notBefore); err != nil {
			return false, err
		}
		payload := map[string]any{
			"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt,
			"next_attempt": c.Ref.Attempt + 1, "outcome": out.Map(),
			"chargeable_failures": newCount,
		}
		if notBefore != nil {
			payload["not_before"] = notBefore.UTC().Format(time.RFC3339)
		}
		if len(artifactIDs) > 0 {
			payload["artifact_ids"] = artifactIDs
		}
		return false, appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseRetried,
			CorrelationID: c.Ref.String(), Payload: payload,
		}})
	}

	// Terminal: attempt -> dead, run -> failed, one transaction.
	n, err := o.svc.finishPhase(ctx, tx, c.PhaseID, fromStatus, PhaseDead, workerID, rawOut)
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil // fenced
	}
	artifactIDs, err := insertArtifacts(ctx, q, c.Ref, gen, res.Artifacts, noParents, now)
	if err != nil {
		return false, err
	}
	if deltaMS > 0 && out.Code != CodeCredentialBusy {
		if _, err := q.AddRunUsage(ctx, store.AddRunUsageParams{ID: c.Ref.RunID, DeltaMs: deltaMS}); err != nil {
			return false, err
		}
	}
	runOut := out
	switch {
	case budgetBreached:
		runOut = Outcome{Class: OutcomePermanent, Code: CodeBudgetExhausted, Message: "execution budget exhausted"}
	case parkedPastDeadline:
		// The wait would outlast the run, so the run dies of the wait
		// itself — nothing was exhausted, and the typed code says which
		// door was shut.
		//
		// PERMANENT, though the outcome that got here was transient. The code
		// is a report of the cause and survives; the class is a statement
		// about what happens next, and for a terminalized run the answer is
		// nothing. ListExpiredRunIDs matches pending/running/waiting only, so
		// no sweep revisits a failed run, while Outcome.Retryable() — which is
		// exactly Class == transient — is what GET /runs/{id} and the
		// run.finished audit detail publish. Carrying the wait's class through
		// told every reader to expect a retry that cannot come. The RetryAt is
		// dropped with it: a dead run has no next instant.
		runOut = Outcome{Class: OutcomePermanent, Code: out.Code, Message: out.Message}
	case out.Retryable(): // retryable but exhausted
		runOut = Outcome{
			Class: OutcomePermanent, Code: CodeAttemptsExhausted,
			Message: fmt.Sprintf("node %s exhausted %d chargeable failures", c.Ref.NodeID, c.MaxAttempts),
		}
	}
	deadPayload := map[string]any{"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt, "outcome": out.Map()}
	if len(artifactIDs) > 0 {
		deadPayload["artifact_ids"] = artifactIDs
	}
	evs := []Event{{
		RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseDead,
		CorrelationID: c.Ref.String(), Payload: deadPayload,
	}}
	return true, o.finishRunTx(ctx, tx, cp, c.Ref.RunID, RunFailed, runOut, EventRunFailed, now, evs)
}

func errorsIsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
