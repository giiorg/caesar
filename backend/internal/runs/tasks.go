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
	ID              uuid.UUID
	TaskID          uuid.UUID
	Status          RunStatus
	DefinitionHash  string
	BaseSHA         string
	Snapshot        PolicySnapshot
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
	WorkerID    string
	Outcome     *Outcome
	CreatedAt   time.Time
	FinishedAt  *time.Time
}

type ArtifactMeta struct {
	ID        uuid.UUID
	NodeID    string
	Attempt   int32
	Type      string
	ParentIDs []uuid.UUID
	CreatedAt time.Time
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
		e = e.WithFields(xerrors.FieldError{Field: "title", Message: fmt.Sprintf("must be 1-%d characters", maxTitleLen)})
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

// resolveIntake pins everything a run needs before any row exists:
// repo, definition (compiled, gates refused until Plan 08), per-node
// harness refs, snapshot, base SHA.
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
	if unsupported := plan.HasUnsupportedNodes(); len(unsupported) > 0 {
		return RepoRef{}, PinnedDefinition{}, "", xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "definition", Message: fmt.Sprintf("gates and publishing arrive in Plan 08 (nodes: %v)", unsupported)})
	}
	for _, id := range plan.Order {
		n := plan.Nodes[id]
		if n.Harness == "" {
			continue
		}
		if _, err := s.harnesses.HarnessByName(ctx, n.Harness); err != nil {
			if xerrors.ClassOf(err) == xerrors.ClassNotFound {
				return RepoRef{}, PinnedDefinition{}, "", xerrors.E(xerrors.ClassInvalid, "validation failed").
					WithFields(xerrors.FieldError{Field: "harness", Message: fmt.Sprintf("node %q references unknown harness %q", id, n.Harness)})
			}
			return RepoRef{}, PinnedDefinition{}, "", err
		}
	}
	baseSHA, err := s.forge.ResolveHead(ctx, repo)
	if err != nil {
		return RepoRef{}, PinnedDefinition{}, "", fmt.Errorf("runs: resolve head: %w", err)
	}
	return repo, def, baseSHA, nil
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
// invariant 12) or refuses a live one, then mints the next run. The
// task's newest run is locked FOR UPDATE: two concurrent reruns
// serialize, the loser sees the winner's pending run and refuses.
func (s *Service) Rerun(ctx context.Context, actor authn.Principal, taskID uuid.UUID) (Run, error) {
	correlationID := httpx.CorrelationID(ctx)
	var run Run
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		taskRow, err := q.GetTask(ctx, taskID)
		if err != nil {
			return notFound(err, "no such task")
		}
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

		// Re-resolve pins fresh (plan decision 4). Version is pinned on
		// the task; look its coordinates up via the definition source.
		repo, err := s.repos.Repo(ctx, taskRow.RepoID)
		if err != nil {
			return err
		}
		def, err := s.definitionByVersionID(ctx, taskRow.WorkflowVersionID)
		if err != nil {
			return err
		}
		baseSHA, err := s.forge.ResolveHead(ctx, repo)
		if err != nil {
			return fmt.Errorf("runs: resolve head: %w", err)
		}
		run, err = s.insertRun(ctx, tx, taskID, taskRow.ProjectID, def, baseSHA, correlationID)
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
// before any row is minted against it.
func (s *Service) definitionByVersionID(ctx context.Context, versionID uuid.UUID) (PinnedDefinition, error) {
	def, err := s.defs.DefinitionByVersionID(ctx, versionID)
	if err != nil {
		return PinnedDefinition{}, err
	}
	if _, err := compilePinned(def); err != nil {
		return PinnedDefinition{}, err
	}
	return def, nil
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
		Status: PhaseStatus(r.Status), WorkerID: worker, Outcome: out,
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}, nil
}

func (s *Service) GetRun(ctx context.Context, id uuid.UUID) (Run, []Phase, []ArtifactMeta, error) {
	q := store.New(s.pool)
	row, err := q.GetRun(ctx, id)
	if err != nil {
		return Run{}, nil, nil, notFound(err, "no such run")
	}
	run, err := runFromRow(row)
	if err != nil {
		return Run{}, nil, nil, err
	}
	phaseRows, err := q.ListPhasesForRun(ctx, id)
	if err != nil {
		return Run{}, nil, nil, fmt.Errorf("runs: list phases: %w", err)
	}
	phases := make([]Phase, 0, len(phaseRows))
	for _, pr := range phaseRows {
		p, err := phaseFromRow(pr)
		if err != nil {
			return Run{}, nil, nil, err
		}
		phases = append(phases, p)
	}
	artRows, err := q.ListArtifactsForRun(ctx, id)
	if err != nil {
		return Run{}, nil, nil, fmt.Errorf("runs: list artifacts: %w", err)
	}
	arts := make([]ArtifactMeta, 0, len(artRows))
	for _, ar := range artRows {
		var parentStrs []string
		if err := json.Unmarshal(ar.ParentIds, &parentStrs); err != nil {
			return Run{}, nil, nil, fmt.Errorf("runs: artifact lineage corrupt: %w", err)
		}
		parents := make([]uuid.UUID, 0, len(parentStrs))
		for _, ps := range parentStrs {
			pid, err := uuid.Parse(ps)
			if err != nil {
				return Run{}, nil, nil, fmt.Errorf("runs: artifact lineage corrupt: %w", err)
			}
			parents = append(parents, pid)
		}
		arts = append(arts, ArtifactMeta{
			ID: ar.ID, NodeID: ar.NodeID, Attempt: ar.Attempt, Type: ar.Type,
			ParentIDs: parents, CreatedAt: ar.CreatedAt,
		})
	}
	return run, phases, arts, nil
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
