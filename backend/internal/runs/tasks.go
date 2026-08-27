package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// Intake validation pins (plan header; bytes, house convention).
const (
	maxTitleLen  = 200
	maxInputsLen = 8 << 10
)

type NewTask struct {
	RepoID          uuid.UUID
	WorkflowID      uuid.UUID
	WorkflowVersion int32
	Title           string
	Inputs          map[string]any
}

type Task struct {
	ID                uuid.UUID
	ProjectID         uuid.UUID
	RepoID            uuid.UUID
	WorkflowVersionID uuid.UUID
	RequesterID       uuid.UUID
	Title             string
	Inputs            map[string]any
	CreatedAt         time.Time
	LatestRunID       uuid.UUID
	LatestRunStatus   RunStatus
}

type Run struct {
	ID             uuid.UUID
	TaskID         uuid.UUID
	Status         RunStatus
	DefinitionHash string
	BaseSHA        string
	Snapshot       PolicySnapshot
	// Generation is the revision round the run is on: 1 until a gate sends
	// work back, then one higher per round.
	Generation      int32
	ExecutionUsedMS int64
	WallDeadline    time.Time
	Outcome         *Outcome
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

type Phase struct {
	ID          uuid.UUID
	NodeID      string
	Attempt     int32
	MaxAttempts int32
	Status      PhaseStatus
	Generation  int32
	WorkerID    string
	Outcome     *Outcome
	CreatedAt   time.Time
	FinishedAt  *time.Time
	// Decision is set only on a gate attempt that was decided. It is joined
	// on the phase row's own id, so a revision's two gate attempts carry
	// their own verdicts rather than sharing the newest.
	Decision *GateDecision
	// Evidence is present ONLY on a gate attempt still awaiting a decision.
	// A decided gate carries its Decision instead, which already names the
	// examined artifact; a non-gate phase has neither.
	Evidence *GateEvidence
}

type ArtifactMeta struct {
	ID         uuid.UUID
	NodeID     string
	Attempt    int32
	Generation int32
	Type       string
	ParentIDs  []uuid.UUID
	CreatedAt  time.Time
}

// ArtifactWithPayload is one artifact including its content. It is separate
// from ArtifactMeta because the run projection deliberately carries metadata
// only: a run with twenty artifacts would otherwise ship every patch on every
// page load.
type ArtifactWithPayload struct {
	ArtifactMeta
	Payload map[string]any
}

// RunDetail is the single-run read: everything the run page renders, from one
// query set. It replaces GetRun's four return values, which had run out of
// room the moment publications joined phases and artifacts.
type RunDetail struct {
	Run Run
	// Labels is what the run page's header renders, and it is filled by the
	// CALLER rather than by GetRunDetail. It lives here because runResponse
	// projects one value, and it is not read from the database here because
	// nothing in the domain needs it: a label is a presentation concern, the
	// port that resolves one is wired only where a request is served, and every
	// service-level reader of a run would otherwise have to wire a label source
	// to observe a phase transition.
	Labels       RunLabels
	Phases       []Phase
	Artifacts    []ArtifactMeta
	Publications []Publication
}

// Publication projects one publish intent. absent_since is deliberately NOT
// carried: it is the settlement sweep's own bookkeeping, it changes on every
// tick, and a UI rendering it would invite a human to act on a probe that is
// designed to be re-run.
type Publication struct {
	NodeID       string
	Status       string
	Branch       string
	TargetBranch string
	BaseSHA      string
	HeadSHA      string
	PRNumber     int64
	PRURL        string
	HaltReason   string
}

type RunEventRecord struct {
	ID         int64
	Kind       string
	Payload    map[string]any
	OccurredAt time.Time
}

func validateNewTask(in NewTask) error {
	e := xerrors.E(xerrors.ClassInvalid, "validation failed")
	ok := true
	if in.Title == "" || len(in.Title) > maxTitleLen {
		e = e.WithFields(xerrors.FieldError{Field: "title", Message: fmt.Sprintf("must be 1-%d bytes", maxTitleLen)})
		ok = false
	}
	if in.WorkflowVersion < 1 {
		e = e.WithFields(xerrors.FieldError{Field: "workflow_version", Message: "must be >= 1"})
		ok = false
	}
	if in.Inputs != nil {
		raw, err := json.Marshal(in.Inputs)
		if err != nil || len(raw) > maxInputsLen {
			e = e.WithFields(xerrors.FieldError{Field: "inputs", Message: fmt.Sprintf("must be a JSON object of at most %d bytes", maxInputsLen)})
			ok = false
		}
	}
	if !ok {
		return e
	}
	return nil
}

// RepoProject resolves a repo to its project for the API's pre-write
// authorization check (run grant on the repo's project, spec §6).
func (s *Service) RepoProject(ctx context.Context, repoID uuid.UUID) (uuid.UUID, error) {
	repo, err := s.repos.Repo(ctx, repoID)
	if err != nil {
		return uuid.Nil, err
	}
	return repo.ProjectID, nil
}

// resolveIntake pins everything a run needs before any row exists: repo,
// definition (compiled — gates and publishes are executable from Plan 08
// on), per-node harness refs, snapshot, base SHA.
func (s *Service) resolveIntake(ctx context.Context, in NewTask) (RepoRef, PinnedDefinition, string, error) {
	repo, err := s.repos.Repo(ctx, in.RepoID)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", err
	}
	def, err := s.defs.Definition(ctx, in.WorkflowID, in.WorkflowVersion)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", err
	}
	plan, err := compilePinned(def)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", err
	}
	if err := s.checkHarnesses(ctx, plan); err != nil {
		return RepoRef{}, PinnedDefinition{}, "", err
	}
	baseSHA, err := s.forge.ResolveHead(ctx, repo)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", fmt.Errorf("runs: resolve head: %w", err)
	}
	return repo, def, baseSHA, nil
}

// checkHarnesses resolves every agentic node's harness against the registry.
// Both intake paths run it: a run whose harness no longer exists can never
// execute, and minting it anyway trades a clear 400 at the door for a typed
// harness_error three phases later.
func (s *Service) checkHarnesses(ctx context.Context, plan ExecPlan) error {
	for _, id := range plan.Order {
		n := plan.Nodes[id]
		if n.Harness == "" {
			continue
		}
		if _, err := s.harnesses.HarnessByName(ctx, n.Harness); err != nil {
			if xerrors.ClassOf(err) == xerrors.ClassNotFound {
				return xerrors.E(xerrors.ClassInvalid, "validation failed").
					WithFields(xerrors.FieldError{Field: "harness", Message: fmt.Sprintf("node %q references unknown harness %q", id, n.Harness)})
			}
			return err
		}
	}
	return nil
}

// insertRun mints one pending run for a task inside tx: snapshot pinned
// as jsonb, budget and wall deadline denormalized for SQL predicates.
func (s *Service) insertRun(ctx context.Context, tx pgx.Tx, taskID, projectID uuid.UUID,
	def PinnedDefinition, baseSHA, correlationID string,
) (Run, error) {
	snap := s.params.Snapshot
	if err := snap.Validate(); err != nil {
		return Run{}, fmt.Errorf("runs: params snapshot: %w", err)
	}
	rawSnap, err := json.Marshal(snap)
	if err != nil {
		return Run{}, fmt.Errorf("runs: marshal snapshot: %w", err)
	}
	now := s.clock.Now()
	row, err := store.New(tx).InsertRun(ctx, store.InsertRunParams{
		TaskID: taskID, DefinitionHash: def.ContentHash, BaseSha: baseSHA,
		PolicySnapshot: rawSnap, ExecutionBudgetMs: snap.ExecutionBudgetMS,
		WallDeadline: now.Add(time.Duration(snap.WallClockMS) * time.Millisecond), CreatedAt: now,
	})
	if err != nil {
		return Run{}, fmt.Errorf("runs: insert run: %w", err)
	}
	if err := appendEvents(ctx, tx, now, []Event{{
		RunID: row.ID, ProjectID: projectID, Kind: EventRunCreated, CorrelationID: correlationID,
		Payload: map[string]any{
			"task_id": taskID.String(), "workflow_version_id": def.VersionID.String(),
			"definition_hash": def.ContentHash, "base_sha": baseSHA,
		},
	}}); err != nil {
		return Run{}, err
	}
	return runFromRow(row)
}

func (s *Service) CreateTask(ctx context.Context, actor authn.Principal, in NewTask) (Task, Run, error) {
	if err := validateNewTask(in); err != nil {
		return Task{}, Run{}, err
	}
	repo, def, baseSHA, err := s.resolveIntake(ctx, in)
	if err != nil {
		return Task{}, Run{}, err
	}
	inputs := in.Inputs
	if inputs == nil {
		inputs = map[string]any{}
	}
	rawInputs, err := json.Marshal(inputs)
	if err != nil {
		return Task{}, Run{}, xerrors.Wrap(xerrors.ClassInvalid, "inputs must be a JSON object", err)
	}

	var task Task
	var run Run
	correlationID := httpx.CorrelationID(ctx)
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		row, err := q.InsertTask(ctx, store.InsertTaskParams{
			ProjectID: repo.ProjectID, RepoID: repo.ID, WorkflowVersionID: def.VersionID,
			Title: in.Title, Inputs: rawInputs, RequesterID: actor.UserID, CreatedAt: s.clock.Now(),
		})
		if err != nil {
			return fmt.Errorf("runs: insert task: %w", err)
		}
		run, err = s.insertRun(ctx, tx, row.ID, repo.ProjectID, def, baseSHA, correlationID)
		if err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "task.created", SubjectKind: "task", SubjectID: row.ID.String(),
			CorrelationID: correlationID,
			Detail:        map[string]any{"title": in.Title, "repo_id": repo.ID.String(), "workflow_version_id": def.VersionID.String()},
		}); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "run.created", SubjectKind: "run", SubjectID: run.ID.String(),
			CorrelationID: correlationID,
		}); err != nil {
			return err
		}
		task = Task{
			ID: row.ID, ProjectID: row.ProjectID, RepoID: row.RepoID, WorkflowVersionID: row.WorkflowVersionID,
			RequesterID: row.RequesterID, Title: row.Title, Inputs: inputs, CreatedAt: row.CreatedAt,
			LatestRunID: run.ID, LatestRunStatus: run.Status,
		}
		return nil
	})
	if err != nil {
		return Task{}, Run{}, err
	}
	return task, run, nil
}

// Rerun supersedes a pending current run (supersede-before-work,
// invariant 12) or refuses a live one, then mints the next run.
//
// Pins are re-resolved through the ports before the transaction opens —
// the same resolve-then-transact shape as CreateTask. A port call from
// inside the transaction would hold a pooled connection, and the run's
// row lock, across a peer-context query or a forge round-trip; since
// those adapters draw on this same pool, a saturated pool would
// self-deadlock. The task's repo and pinned version are immutable after
// intake, so reading them up front is safe.
//
// Inside the transaction the task's newest run is locked FOR UPDATE for
// the whole supersede-then-insert decision: two concurrent reruns
// serialize, and the loser sees the winner's pending run and refuses.
func (s *Service) Rerun(ctx context.Context, actor authn.Principal, taskID uuid.UUID) (Run, error) {
	taskRow, err := store.New(s.pool).GetTask(ctx, taskID)
	if err != nil {
		return Run{}, notFound(err, "no such task")
	}
	// Re-resolve pins fresh (plan decision 4). Version is pinned on the
	// task; look its coordinates up via the definition source.
	repo, err := s.repos.Repo(ctx, taskRow.RepoID)
	if err != nil {
		return Run{}, err
	}
	def, plan, err := s.definitionByVersionID(ctx, taskRow.WorkflowVersionID)
	if err != nil {
		return Run{}, err
	}
	if err := s.checkHarnesses(ctx, plan); err != nil {
		return Run{}, err
	}
	baseSHA, err := s.forge.ResolveHead(ctx, repo)
	if err != nil {
		return Run{}, fmt.Errorf("runs: resolve head: %w", err)
	}

	correlationID := httpx.CorrelationID(ctx)
	var run Run
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		latest, err := q.LatestRunForUpdate(ctx, taskID)
		if err != nil {
			return fmt.Errorf("runs: latest run: %w", err)
		}
		now := s.clock.Now()
		switch RunStatus(latest.Status) {
		case RunPending:
			superseded := Outcome{Class: OutcomePermanent, Code: CodeSuperseded, Message: "superseded by re-run"}
			raw, err := json.Marshal(superseded)
			if err != nil {
				return fmt.Errorf("runs: marshal outcome: %w", err)
			}
			n, err := q.FinishRun(ctx, store.FinishRunParams{
				ID: latest.ID, ToStatus: string(RunSuperseded), Outcome: raw, FinishedAt: &now,
			})
			if err != nil {
				return fmt.Errorf("runs: supersede: %w", err)
			}
			if n == 0 {
				return fmt.Errorf("runs: supersede: run %s left its live state under lock", latest.ID)
			}
			if _, err := q.SupersedeLivePhases(ctx, store.SupersedeLivePhasesParams{
				RunID: latest.ID, Outcome: raw, FinishedAt: &now,
			}); err != nil {
				return fmt.Errorf("runs: supersede phases: %w", err)
			}
			if err := s.audit.Record(ctx, tx, audit.Entry{
				Actor: actorID(actor), Action: "run.superseded", SubjectKind: "run", SubjectID: latest.ID.String(),
				CorrelationID: correlationID,
			}); err != nil {
				return err
			}
		case RunRunning, RunWaiting:
			return xerrors.E(xerrors.ClassConflict, "run is in flight; cancel arrives in Plan 09")
		}

		run, err = s.insertRun(ctx, tx, taskID, taskRow.ProjectID, def, baseSHA, correlationID)
		if isUniqueViolation(err) {
			// runs_task_live_key: another rerun committed its pending run
			// between our LatestRunForUpdate and this insert. That is a
			// conflict, not an internal failure — the caller should retry or
			// look at the run that won.
			return xerrors.E(xerrors.ClassConflict, "another re-run of this task is already pending")
		}
		if err != nil {
			return err
		}
		if RunStatus(latest.Status) == RunPending {
			if err := appendEvents(ctx, tx, now, []Event{{
				RunID: latest.ID, ProjectID: taskRow.ProjectID, Kind: EventRunSuperseded,
				CorrelationID: correlationID, Payload: map[string]any{"by_run_id": run.ID.String()},
			}}); err != nil {
				return err
			}
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Actor: actorID(actor), Action: "run.created", SubjectKind: "run", SubjectID: run.ID.String(),
			CorrelationID: correlationID,
		})
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

// definitionByVersionID re-resolves a task's pinned version through the
// port's second method; the compile pass rejects stored corruption
// before any row is minted against it. The compiled plan comes back with
// the definition so the re-run intake's harness check does not compile it
// a second time.
func (s *Service) definitionByVersionID(ctx context.Context, versionID uuid.UUID) (PinnedDefinition, ExecPlan, error) {
	def, err := s.defs.DefinitionByVersionID(ctx, versionID)
	if err != nil {
		return PinnedDefinition{}, ExecPlan{}, err
	}
	plan, err := compilePinned(def)
	if err != nil {
		return PinnedDefinition{}, ExecPlan{}, err
	}
	return def, plan, nil
}

func runFromRow(r store.Run) (Run, error) {
	snap, err := snapshotFromRow(r.PolicySnapshot)
	if err != nil {
		return Run{}, err
	}
	out, err := outcomeFromRow(r.Outcome)
	if err != nil {
		return Run{}, err
	}
	return Run{
		ID: r.ID, TaskID: r.TaskID, Status: RunStatus(r.Status),
		DefinitionHash: r.DefinitionHash, BaseSHA: r.BaseSha, Snapshot: snap,
		Generation:      r.Generation,
		ExecutionUsedMS: r.ExecutionUsedMs, WallDeadline: r.WallDeadline, Outcome: out,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}, nil
}

func taskFromRow(t store.Task, latestRunID uuid.UUID, latestStatus string) (Task, error) {
	inputs, err := unmarshalMap(t.Inputs)
	if err != nil {
		return Task{}, err
	}
	return Task{
		ID: t.ID, ProjectID: t.ProjectID, RepoID: t.RepoID, WorkflowVersionID: t.WorkflowVersionID,
		RequesterID: t.RequesterID, Title: t.Title, Inputs: inputs, CreatedAt: t.CreatedAt,
		LatestRunID: latestRunID, LatestRunStatus: RunStatus(latestStatus),
	}, nil
}

func (s *Service) GetTask(ctx context.Context, id uuid.UUID) (Task, error) {
	q := store.New(s.pool)
	row, err := q.GetTask(ctx, id)
	if err != nil {
		return Task{}, notFound(err, "no such task")
	}
	latest, err := q.ListRunsForTask(ctx, id)
	if err != nil {
		return Task{}, fmt.Errorf("runs: list runs: %w", err)
	}
	// Intake commits a task and its first run together, so this is
	// corruption rather than a state a caller can reach.
	if len(latest) == 0 {
		return Task{}, fmt.Errorf("runs: task %s has no runs", id)
	}
	return taskFromRow(row, latest[0].ID, latest[0].Status)
}

func (s *Service) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := store.New(s.pool).ListTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("runs: list tasks: %w", err)
	}
	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		t, err := taskFromRow(r.Task, r.LatestRunID, r.LatestRunStatus)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (s *Service) ListTasksByProjects(ctx context.Context, ids []uuid.UUID) ([]Task, error) {
	if len(ids) == 0 {
		return []Task{}, nil
	}
	rows, err := store.New(s.pool).ListTasksByProjects(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("runs: list tasks by projects: %w", err)
	}
	out := make([]Task, 0, len(rows))
	for _, r := range rows {
		t, err := taskFromRow(r.Task, r.LatestRunID, r.LatestRunStatus)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func phaseFromRow(r store.PhaseRun) (Phase, error) {
	out, err := outcomeFromRow(r.Outcome)
	if err != nil {
		return Phase{}, err
	}
	worker := ""
	if r.WorkerID.Valid {
		worker = r.WorkerID.String
	}
	return Phase{
		ID: r.ID, NodeID: r.NodeID, Attempt: r.Attempt, MaxAttempts: r.MaxAttempts,
		Status: PhaseStatus(r.Status), Generation: r.Generation, WorkerID: worker, Outcome: out,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}, nil
}

// GetRun is GetRunDetail for the callers that only ever wanted the first three
// values. It stays because it has twenty-seven of them across three build
// lanes, and widening a signature they do not read would be churn, not clarity.
func (s *Service) GetRun(ctx context.Context, id uuid.UUID) (Run, []Phase, []ArtifactMeta, error) {
	d, err := s.GetRunDetail(ctx, id)
	if err != nil {
		return Run{}, nil, nil, err
	}
	return d.Run, d.Phases, d.Artifacts, nil
}

// GetRunSummary reads the run row alone. It is what a caller that only has to
// authorize against the run — the event poll, on every tick — should pay for;
// GetRunDetail's five queries exist for the page, not for the loop.
func (s *Service) GetRunSummary(ctx context.Context, id uuid.UUID) (Run, error) {
	row, err := store.New(s.pool).GetRun(ctx, id)
	if err != nil {
		return Run{}, notFound(err, "no such run")
	}
	return runFromRow(row)
}

// GetRunDetail is the single-run read behind GET /runs/{id}.
func (s *Service) GetRunDetail(ctx context.Context, id uuid.UUID) (RunDetail, error) {
	q := store.New(s.pool)
	row, err := q.GetRun(ctx, id)
	if err != nil {
		return RunDetail{}, notFound(err, "no such run")
	}
	run, err := runFromRow(row)
	if err != nil {
		return RunDetail{}, err
	}
	phaseRows, err := q.ListPhasesForRun(ctx, id)
	if err != nil {
		return RunDetail{}, fmt.Errorf("runs: list phases: %w", err)
	}
	decisions, err := q.ListGateDecisionsForRun(ctx, id)
	if err != nil {
		return RunDetail{}, fmt.Errorf("runs: list gate decisions: %w", err)
	}
	byPhase := make(map[uuid.UUID]GateDecision, len(decisions))
	for _, d := range decisions {
		byPhase[d.PhaseRunID] = gateDecisionFromRow(d)
	}
	phases := make([]Phase, 0, len(phaseRows))
	for _, pr := range phaseRows {
		p, err := phaseFromRow(pr)
		if err != nil {
			return RunDetail{}, err
		}
		if d, ok := byPhase[pr.ID]; ok {
			p.Decision = &d
		}
		phases = append(phases, p)
	}
	if err := s.attachGateEvidence(ctx, q, run, phases); err != nil {
		return RunDetail{}, err
	}
	artRows, err := q.ListArtifactsForRun(ctx, id)
	if err != nil {
		return RunDetail{}, fmt.Errorf("runs: list artifacts: %w", err)
	}
	arts := make([]ArtifactMeta, 0, len(artRows))
	for _, ar := range artRows {
		parents, err := parentIDsFromRow(ar.ParentIds)
		if err != nil {
			return RunDetail{}, err
		}
		arts = append(arts, ArtifactMeta{
			ID: ar.ID, NodeID: ar.NodeID, Attempt: ar.Attempt, Generation: ar.Generation,
			Type: ar.Type, ParentIDs: parents, CreatedAt: ar.CreatedAt,
		})
	}
	intents, err := q.ListPublishIntentsForRun(ctx, id)
	if err != nil {
		return RunDetail{}, fmt.Errorf("runs: list publish intents: %w", err)
	}
	pubs := make([]Publication, 0, len(intents))
	for _, pi := range intents {
		pubs = append(pubs, publicationFromRow(pi))
	}
	return RunDetail{Run: run, Phases: phases, Artifacts: arts, Publications: pubs}, nil
}

// parentIDsFromRow decodes an artifact's lineage column — jsonb NOT NULL
// DEFAULT '[]', an array of uuid strings. A row that fails either half is
// corruption, not a caller's mistake: nothing writes that column but this
// system, which is why both failures are plain errors and neither is a 400.
//
// It returns an empty slice rather than nil for a lineage-less artifact, so the
// two projections that call it both put [] on the wire and never null.
//
// Extracted from GetRunDetail so the payload read below decodes lineage the same
// way: a client reads provenance off both responses, and one decoder is one
// answer.
func parentIDsFromRow(raw []byte) ([]uuid.UUID, error) {
	var strs []string
	if err := json.Unmarshal(raw, &strs); err != nil {
		return nil, fmt.Errorf("runs: artifact lineage corrupt: %w", err)
	}
	out := make([]uuid.UUID, 0, len(strs))
	for _, s := range strs {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("runs: artifact lineage corrupt: %w", err)
		}
		out = append(out, id)
	}
	return out, nil
}

// ArtifactPayload reads one artifact of one run.
//
// The RUN ID IS PART OF THE PREDICATE and not merely of the path. Authorization
// upstream is scoped on the run, so an artifact fetched by bare id would let
// one project's run id authorize another project's artifact. Belt and braces:
// the caller checks the run, this checks the artifact belongs to it.
func (s *Service) ArtifactPayload(ctx context.Context, runID, artifactID uuid.UUID) (ArtifactWithPayload, error) {
	row, err := store.New(s.pool).GetArtifactByID(ctx, artifactID)
	if err != nil {
		return ArtifactWithPayload{}, notFound(err, "no such artifact")
	}
	if row.RunID != runID {
		// 404 rather than 403: the caller has been authorized for THIS run, and
		// telling them an id exists elsewhere is an existence oracle.
		return ArtifactWithPayload{}, xerrors.E(xerrors.ClassNotFound, "no such artifact")
	}
	payload, err := unmarshalMap(row.Payload)
	if err != nil {
		return ArtifactWithPayload{}, err
	}
	// unmarshalMap is nil-safe for an EMPTY column but not for the jsonb literal
	// `null`, which encoding/json stores into a map by setting it to nil — the
	// "null has no effect" rule covers primitives, not maps. `payload` is jsonb
	// NOT NULL, which forbids SQL NULL and permits 'null'::jsonb, and the write
	// path reaches it: declaredArtifact.Payload has no presence check
	// (runner/collect.go), so a harness declaring {"type":"plan"} with no payload
	// key marshals a nil map to the bytes `null`.
	//
	// An artifact with no content is an EMPTY payload, not an absent one. Pinning
	// that here rather than in the DTO keeps the guarantee on the domain type, so
	// no second projection of this value has to rediscover it.
	if payload == nil {
		payload = map[string]any{}
	}
	parents, err := parentIDsFromRow(row.ParentIds)
	if err != nil {
		return ArtifactWithPayload{}, err
	}
	return ArtifactWithPayload{
		ArtifactMeta: ArtifactMeta{
			ID: row.ID, NodeID: row.NodeID, Attempt: row.Attempt, Generation: row.Generation,
			Type: row.Type, ParentIDs: parents, CreatedAt: row.CreatedAt,
		},
		Payload: payload,
	}, nil
}

// attachGateEvidence fills in Evidence on every phase still awaiting a
// decision, in place.
//
// It compiles the run's plan at most once — all phases of a run share one
// definition — and does so ONLY when such a phase exists: a run with no waiting
// gate must not pay for a task read and a definition read on every page load,
// and most runs on most pages have none. The deciding clock is read in the same
// lazy block for the same reason, and once rather than per phase, so two gates
// of one run cannot disagree about which side of the run's deadline they are on.
func (s *Service) attachGateEvidence(ctx context.Context, q *store.Queries, run Run, phases []Phase) error {
	var plan ExecPlan
	var requesterID uuid.UUID
	var asOf time.Time
	planLoaded := false
	for i, p := range phases {
		if p.Status != PhaseWaitingDecision {
			continue
		}
		if !planLoaded {
			task, err := q.GetTask(ctx, run.TaskID)
			if err != nil {
				return err
			}
			def, err := s.defs.DefinitionByVersionID(ctx, task.WorkflowVersionID)
			if err != nil {
				return err
			}
			if plan, err = compilePinned(def); err != nil {
				return err
			}
			// From the DATABASE, because it is the other half of every
			// wall_deadline comparison below and s.clock.Now() on that side
			// would let app-clock skew move the boundary (queries.sql:114-119).
			if asOf, err = q.DBNow(ctx); err != nil {
				return fmt.Errorf("runs: read the deciding clock: %w", err)
			}
			requesterID = task.RequesterID
			planLoaded = true
		}
		ev, err := gateEvidence(ctx, q, gateEvidenceInput{
			plan: plan, runID: run.ID, nodeID: p.NodeID, snapshot: run.Snapshot,
			requesterID: requesterID, wallDeadline: run.WallDeadline, asOf: asOf,
		})
		if err != nil {
			return err
		}
		phases[i].Evidence = &ev
	}
	return nil
}

// publicationFromRow flattens the receipt columns. Each is NULL until its
// effect landed, and the zero value is exactly what an absent receipt means.
func publicationFromRow(r store.PublishIntent) Publication {
	return Publication{
		NodeID: r.NodeID, Status: r.Status, Branch: r.Branch,
		TargetBranch: r.TargetBranch, BaseSHA: r.BaseSha,
		HeadSHA: r.HeadSha.String, PRNumber: r.PrNumber.Int64,
		PRURL: r.PrUrl.String, HaltReason: r.HaltReason.String,
	}
}

func (s *Service) ListRunsForTask(ctx context.Context, taskID uuid.UUID) ([]Run, error) {
	q := store.New(s.pool)
	if _, err := q.GetTask(ctx, taskID); err != nil {
		return nil, notFound(err, "no such task")
	}
	rows, err := q.ListRunsForTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("runs: list runs: %w", err)
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		run, err := runFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// eventPageCap bounds the poll endpoint until SSE lands in Plan 10.
const eventPageCap = 500

func (s *Service) ListRunEvents(ctx context.Context, runID uuid.UUID, afterID int64, limit int32) ([]RunEventRecord, error) {
	q := store.New(s.pool)
	if _, err := q.GetRun(ctx, runID); err != nil {
		return nil, notFound(err, "no such run")
	}
	if limit < 1 || limit > eventPageCap {
		limit = eventPageCap
	}
	rows, err := q.ListRunEventsAfter(ctx, store.ListRunEventsAfterParams{
		RunID: runID, ID: afterID, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("runs: list events: %w", err)
	}
	out := make([]RunEventRecord, 0, len(rows))
	for _, r := range rows {
		payload, err := unmarshalMap(r.Payload)
		if err != nil {
			return nil, err
		}
		out = append(out, RunEventRecord{ID: r.ID, Kind: r.Kind, Payload: payload, OccurredAt: r.OccurredAt})
	}
	return out, nil
}
