package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

type OrchestratorConfig struct {
	Workers           int
	PollInterval      time.Duration
	LeaseTTL          time.Duration
	HeartbeatInterval time.Duration
	OutboxRetention   time.Duration
}

// Orchestrator drives the queue: one janitor goroutine (run startup,
// deadline sweep, lease sweep + reconcile, outbox retention) and N claim
// workers. It owns no state Postgres doesn't own first (spec §1).
type Orchestrator struct {
	svc       *Service
	exec      Executor
	admission Admission
	cfg       OrchestratorConfig
	log       *slog.Logger
	workerTag string
	lastPrune time.Time

	planMu sync.Mutex
	plans  map[uuid.UUID]cachedPlan // run id -> compiled plan + context
}

type cachedPlan struct {
	plan       ExecPlan
	projectID  uuid.UUID
	taskInputs map[string]any
	baseSHA    string
	repo       RepoRef
}

func NewOrchestrator(svc *Service, exec Executor, admission Admission, cfg OrchestratorConfig, log *slog.Logger) *Orchestrator {
	if admission == nil {
		admission = AlwaysAdmit{}
	}
	return &Orchestrator{
		svc: svc, exec: exec, admission: admission, cfg: cfg, log: log,
		workerTag: uuid.NewString()[:8],
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
		if err := o.sweepExpiredRuns(ctx); err != nil {
			o.log.Error("orchestrator: wall-deadline sweep", "error", err)
		}
		if err := o.sweepExpiredLeases(ctx); err != nil {
			o.log.Error("orchestrator: lease sweep", "error", err)
		}
		if err := o.sweepStuckReconciling(ctx); err != nil {
			o.log.Error("orchestrator: stuck-reconciling sweep", "error", err)
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
	}
	o.planMu.Lock()
	o.plans[runID] = cp
	o.planMu.Unlock()
	return cp, nil
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
				if _, err := o.svc.insertPhaseTx(ctx, tx, runID, root, 1, maxAttempts); err != nil {
					return err
				}
				evs = append(evs, Event{
					RunID: runID, ProjectID: cp.projectID, Kind: EventPhaseMaterialized,
					CorrelationID: AttemptRef{RunID: runID, NodeID: root, Attempt: 1}.String(),
					Payload:       map[string]any{"node_id": root, "attempt": 1},
				})
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
func (o *Orchestrator) sweepExpiredRuns(ctx context.Context) error {
	ids, err := store.New(o.svc.pool).ListExpiredRunIDs(ctx, 16)
	if err != nil {
		return err
	}
	for _, runID := range ids {
		out := Outcome{Class: OutcomePermanent, Code: CodeWallDeadlineExceeded, Message: "wall clock expired"}
		if err := o.terminalizeRun(ctx, runID, RunDead, out, EventRunDead); err != nil {
			o.log.Error("orchestrator: expire run", "run", runID, "error", err)
		}
	}
	return nil
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
		o.reconcileAttempt(ctx, row, true)
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
		o.reconcileAttempt(ctx, row, false)
	}
	return nil
}

// reconcileAttempt runs reconcile-before-retry for one row already in
// 'reconciling' (spec §3): adopt a completed execution idempotently,
// anything else concludes transient reconcile_lost. announce writes the
// phase.reconciling event — true for the lease sweep, which is what put
// the row in the state, false for the stuck sweep, where the process
// that died already announced it.
func (o *Orchestrator) reconcileAttempt(ctx context.Context, row store.PhaseRun, announce bool) {
	cp, err := o.loadPlan(ctx, row.RunID)
	if err != nil {
		o.log.Error("orchestrator: reconcile plan load", "phase", row.ID, "error", err)
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
			o.log.Error("orchestrator: reconciling event", "phase", row.ID, "error", err)
		}
	}

	ins, err := o.exec.Inspect(ctx, ref)
	if err != nil || !ins.Completed {
		out := Outcome{Class: OutcomeTransient, Code: CodeReconcileLost, Message: "attempt lost its lease with no adoptable result"}
		o.evaluate(ctx, "", cp, claimedFromRow(row), PhaseReconciling, ExecutionResult{}, OutcomeError{Outcome: out})
		return
	}
	// Completed with intact outputs: adopt idempotently.
	o.evaluate(ctx, "", cp, claimedFromRow(row), PhaseReconciling, ins.Result, nil)
}

func claimedFromRow(row store.PhaseRun) claimed {
	claimedAt := time.Time{}
	if row.ClaimedAt != nil {
		claimedAt = *row.ClaimedAt
	}
	return claimed{
		PhaseID:     row.ID,
		Ref:         AttemptRef{RunID: row.RunID, NodeID: row.NodeID, Attempt: row.Attempt},
		MaxAttempts: row.MaxAttempts,
		ClaimedAt:   claimedAt,
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

	// Deadline = the tighter of the two clocks (spec §3 Budgets).
	deadline := c.RunWallDeadline
	if rem := RemainingBudgetMS(c.BudgetMS, c.UsedMS); rem > 0 {
		if budgetEnd := now.Add(time.Duration(rem) * time.Millisecond); budgetEnd.Before(deadline) {
			deadline = budgetEnd
		}
	}
	execCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	// Heartbeat until execution finishes; a fenced heartbeat cancels.
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

	req, err := o.buildRequest(ctx, cp, node, c.Ref, deadline)
	var res ExecutionResult
	if err == nil {
		res, err = o.exec.Execute(execCtx, req)
	}
	close(hbDone)
	o.evaluate(ctx, workerID, cp, c, PhaseRunning, res, err)
}

// buildRequest assembles explicit inputs: latest artifacts of each input
// origin, harness resolved fresh against the registry.
func (o *Orchestrator) buildRequest(ctx context.Context, cp cachedPlan, node PlanNode, ref AttemptRef, deadline time.Time) (ExecutionRequest, error) {
	req := ExecutionRequest{
		Ref: ref, Kind: node.Kind, Repo: cp.repo, Instructions: node.Instructions,
		TaskInputs: cp.taskInputs, BaseSHA: cp.baseSHA, Deadline: deadline,
	}
	if node.Harness != "" {
		h, err := o.svc.harnesses.HarnessByName(ctx, node.Harness)
		if err != nil {
			return ExecutionRequest{}, OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodeHarnessError,
				Message: fmt.Sprintf("harness %q no longer registered", node.Harness),
			}}
		}
		req.HarnessID = h.ID
	}
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
	return req, nil
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

	// Persist artifacts with lineage = this node's input artifact ids.
	node := cp.plan.Nodes[c.Ref.NodeID]
	var parentIDs []string
	for _, in := range node.Inputs {
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{RunID: c.Ref.RunID, NodeID: in.Origin, Type: in.Artifact})
		if err == nil {
			parentIDs = append(parentIDs, row.ID.String())
		}
	}
	if parentIDs == nil {
		parentIDs = []string{}
	}
	rawParents, err := json.Marshal(parentIDs)
	if err != nil {
		return false, err
	}
	artifactIDs, err := insertArtifacts(ctx, q, c.Ref, res.Artifacts, rawParents, now)
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

	// Materialize newly-ready successors (readiness from succeeded set).
	succeededNodes, err := q.ListSucceededNodes(ctx, c.Ref.RunID)
	if err != nil {
		return false, err
	}
	succeeded := map[string]bool{}
	for _, id := range succeededNodes {
		succeeded[id] = true
	}
	attempted := map[string]bool{}
	phaseRows, err := q.ListPhasesForRun(ctx, c.Ref.RunID)
	if err != nil {
		return false, err
	}
	for _, pr := range phaseRows {
		attempted[pr.NodeID] = true
	}
	for _, nodeID := range cp.plan.Order {
		if attempted[nodeID] || !cp.plan.Ready(nodeID, succeeded) {
			continue
		}
		if _, err := o.svc.insertPhaseTx(ctx, tx, c.Ref.RunID, nodeID, 1, c.MaxAttempts); err != nil {
			return false, err
		}
		evs = append(evs, Event{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPhaseMaterialized,
			CorrelationID: AttemptRef{RunID: c.Ref.RunID, NodeID: nodeID, Attempt: 1}.String(),
			Payload:       map[string]any{"node_id": nodeID, "attempt": 1},
		})
	}

	// Completion: every node succeeded.
	if len(succeeded) == len(cp.plan.Nodes) {
		return true, o.finishRunTx(ctx, tx, cp, c.Ref.RunID, RunSucceeded, Outcome{}, EventRunSucceeded, now, evs)
	}
	return false, appendEvents(ctx, tx, now, evs)
}

// insertArtifacts persists one attempt's work products under the
// caller's tx and returns their ids for the event payload. Lineage is
// the caller's call: successes descend from their inputs, failures from
// nothing.
func insertArtifacts(ctx context.Context, q *store.Queries, ref AttemptRef,
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
			Type: a.Type, Payload: rawPayload, ParentIds: rawParents, CreatedAt: now,
		}); err != nil {
			return nil, fmt.Errorf("runs: insert artifact: %w", err)
		}
		ids = append(ids, id.String())
	}
	return ids, nil
}

// finishRunTx completes/fails the run inside the caller's tx, appending
// the accumulated events first so ordering reads naturally.
func (o *Orchestrator) finishRunTx(ctx context.Context, tx pgx.Tx, cp cachedPlan, runID uuid.UUID,
	to RunStatus, out Outcome, eventKind string, now time.Time, evs []Event,
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
		RunID: runID, ProjectID: cp.projectID, Kind: eventKind,
		CorrelationID: "caesar-run-" + runID.String(), Payload: payload,
	})
	if err := appendEvents(ctx, tx, now, evs); err != nil {
		return err
	}
	detail := map[string]any{"status": string(to)}
	if to != RunSucceeded {
		detail["outcome"] = out.Map()
	}
	return o.svc.audit.Record(ctx, tx, audit.Entry{
		Action: "run.finished", SubjectKind: "run", SubjectID: runID.String(), Detail: detail,
	})
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
	retry := out.Retryable() && c.Ref.Attempt < c.MaxAttempts
	budgetBreached := false
	if retry {
		// The fencing CAS must pick failed vs dead up front (failed ->
		// dead is not a legal transition), and a delta that crosses the
		// budget must not insert a successor the claim predicate
		// (execution_used_ms < execution_budget_ms) would refuse forever.
		// The run-row lock makes the read race-free; usage itself is
		// still only added after the CAS wins.
		runRow, err := q.GetRunForUpdate(ctx, c.Ref.RunID)
		if err != nil {
			return false, err
		}
		if runRow.ExecutionUsedMs+max(deltaMS, 0) >= runRow.ExecutionBudgetMs {
			retry, budgetBreached = false, true
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
		artifactIDs, err := insertArtifacts(ctx, q, c.Ref, res.Artifacts, noParents, now)
		if err != nil {
			return false, err
		}
		if deltaMS > 0 {
			if _, err := q.AddRunUsage(ctx, store.AddRunUsageParams{ID: c.Ref.RunID, DeltaMs: deltaMS}); err != nil {
				return false, err
			}
		}
		if _, err := o.svc.insertPhaseTx(ctx, tx, c.Ref.RunID, c.Ref.NodeID, c.Ref.Attempt+1, c.MaxAttempts); err != nil {
			return false, err
		}
		payload := map[string]any{
			"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt,
			"next_attempt": c.Ref.Attempt + 1, "outcome": out.Map(),
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
	artifactIDs, err := insertArtifacts(ctx, q, c.Ref, res.Artifacts, noParents, now)
	if err != nil {
		return false, err
	}
	if deltaMS > 0 {
		if _, err := q.AddRunUsage(ctx, store.AddRunUsageParams{ID: c.Ref.RunID, DeltaMs: deltaMS}); err != nil {
			return false, err
		}
	}
	runOut := out
	switch {
	case budgetBreached:
		runOut = Outcome{Class: OutcomePermanent, Code: CodeBudgetExhausted, Message: "execution budget exhausted"}
	case out.Retryable(): // retryable but exhausted
		runOut = Outcome{
			Class: OutcomePermanent, Code: CodeAttemptsExhausted,
			Message: fmt.Sprintf("node %s exhausted %d attempts", c.Ref.NodeID, c.MaxAttempts),
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
