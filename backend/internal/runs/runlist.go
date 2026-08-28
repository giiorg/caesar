package runs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// defaultRunPageLimit and maxRunPageLimit bound one page. This endpoint is
// paginated FROM THE OUTSET because it is new: GET /tasks and GET /approvals
// already have the cliff, and there is no reason to build a third.
const (
	defaultRunPageLimit int32 = 50
	maxRunPageLimit     int32 = 200
)

// PhaseSummary is one dot in a run card's phase row.
//
// Status is a POINTER because "the orchestrator has not materialized this node
// yet" is not a phase status. `pending` means a row exists and is claimable;
// null means no row exists at all, and only the client that knows the
// difference can draw a dim dot rather than a queued one.
//
// NodeType rides along because a gate dot is drawn differently from a work dot
// and a client must not infer a node's type from its id string.
type PhaseSummary struct {
	NodeID     string
	NodeType   string
	Status     *PhaseStatus
	Attempt    int32
	Generation int32
}

// RunSummary is a run as a LIST ROW. It is a different projection from
// RunResponse and that is the point: everything a Home or Tasks card renders is
// here, so the card costs no second request (spec §2.1).
type RunSummary struct {
	ID          uuid.UUID
	TaskID      uuid.UUID
	TaskTitle   string
	ProjectID   uuid.UUID
	ProjectName string
	RepoSlug    string
	// WorkflowName is the workflow's current name, resolved through the pinned
	// VERSION. A rename is therefore visible on historical runs, which is the
	// honest behaviour: the workflow is the thing that was renamed.
	WorkflowName string
	Status       RunStatus
	// Attempt is the attempt ordinal of the newest phase row on this run — what
	// a card means by "attempt 2". A run has no attempt of its own; attempts
	// are per node, and the per-node ones are in Phases.
	Attempt     int32
	Generation  int32
	RequesterID uuid.UUID
	// Three real columns. `runs` has no updated_at, and synthesising one would
	// mean choosing which of these the client meant; the card renders
	// finished_at ?? started_at ?? created_at and the choice stays visible.
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Phases     []PhaseSummary
}

// RunListFilter is one page request. All is the admin view; ProjectIDs is the
// exact grant set otherwise, and an empty one with All false is an empty page
// rather than everything.
type RunListFilter struct {
	All        bool
	ProjectIDs []uuid.UUID
	Status     string
	TaskID     *uuid.UUID
	Limit      int32
	Cursor     string
}

// phaseSummaries renders the run's whole shape: every node of the compiled
// definition, in topological order, whether or not it has a row yet.
//
// EVERY node, not only the materialized ones. The locked cards show pending
// dots ahead of the active one, and a list that grew as the run advanced would
// render a two-node run and a ten-node run identically until the ninth phase.
func phaseSummaries(plan ExecPlan, live map[string]PhaseSummary) []PhaseSummary {
	out := make([]PhaseSummary, 0, len(plan.Order))
	for _, id := range plan.Order {
		entry := PhaseSummary{NodeID: id, NodeType: plan.Nodes[id].Kind}
		if got, ok := live[id]; ok {
			entry.Status, entry.Attempt, entry.Generation = got.Status, got.Attempt, got.Generation
		}
		out = append(out, entry)
	}
	return out
}

func clampRunPageLimit(n int32) int32 {
	switch {
	case n <= 0:
		return defaultRunPageLimit
	case n > maxRunPageLimit:
		return maxRunPageLimit
	default:
		return n
	}
}

func (f RunListFilter) validate() error {
	if f.Status != "" && !RunStatus(f.Status).Valid() {
		return xerrors.E(xerrors.ClassInvalid, "validation failed").
			WithFields(xerrors.FieldError{Field: "status", Message: "not a run status"})
	}
	return nil
}

// RunLabels are the display labels a single run's projections carry. They are
// the same three strings a list row already gets in batch, resolved for one run
// — the run page's sticky header has no other source for them, and discovering
// that in the web slice would reopen a closed contract.
type RunLabels struct {
	TaskTitle    string
	RepoSlug     string
	WorkflowName string
}

// labelsFor resolves one task's labels through the batched port.
//
// LOUD, NOT DEGRADED, when the port is missing. A list that silently renders
// ids where names belong looks like a design choice to everyone who sees it and
// like a bug to nobody.
//
// A label the port does not know stays empty rather than failing the read: the
// port's contract is that a missing id is an absent entry (a repository deleted
// after its runs finished must not take out the page that shows them).
func (s *Service) labelsFor(ctx context.Context, title string, repoID, versionID uuid.UUID) (RunLabels, error) {
	if s.params.Labels == nil {
		return RunLabels{}, fmt.Errorf("runs: the run label source is not wired")
	}
	slugs, err := s.params.Labels.RepoSlugs(ctx, []uuid.UUID{repoID})
	if err != nil {
		return RunLabels{}, fmt.Errorf("runs: resolve repo label: %w", err)
	}
	names, err := s.params.Labels.WorkflowNames(ctx, []uuid.UUID{versionID})
	if err != nil {
		return RunLabels{}, fmt.Errorf("runs: resolve workflow label: %w", err)
	}
	return RunLabels{TaskTitle: title, RepoSlug: slugs[repoID], WorkflowName: names[versionID]}, nil
}

// LabelsForTask is labelsFor for a caller that already holds the task — the two
// shallow run projections, which build their RunDetail themselves.
func (s *Service) LabelsForTask(ctx context.Context, t Task) (RunLabels, error) {
	return s.labelsFor(ctx, t.Title, t.RepoID, t.WorkflowVersionID)
}

// ListRuns is one page of the run list: the projection a Home or Tasks card
// renders in full, so a card costs no second request.
//
// Every step below is one round trip PER PAGE and not per row — the page, its
// phase rows, one compiled plan per distinct workflow version, and three
// batched label lookups. The per-row shape is the N+1 this endpoint exists to
// remove, and putting it here rather than in the client only moves it somewhere
// harder to see.
func (s *Service) ListRuns(ctx context.Context, f RunListFilter) ([]RunSummary, string, error) {
	if s.params.Labels == nil {
		return nil, "", fmt.Errorf("runs: the run label source is not wired")
	}
	if err := f.validate(); err != nil {
		return nil, "", err
	}
	limit := clampRunPageLimit(f.Limit)
	params := store.ListRunsPageParams{
		AllProjects: f.All,
		// A nil slice encodes as SQL NULL, and `x = ANY(NULL)` is NULL rather
		// than false — the same empty page by accident. Normalizing keeps the
		// empty-grant-set answer a property of `= ANY('{}')`, which is what the
		// query's own comment claims it is.
		ProjectIds: append([]uuid.UUID{}, f.ProjectIDs...),
		// One extra row is how the next page is DETECTED. Counting is the
		// alternative and it is a second query over the same predicate.
		PageLimit: limit + 1,
	}
	if f.Status != "" {
		params.Status = pgtype.Text{String: f.Status, Valid: true}
	}
	if f.TaskID != nil {
		params.TaskID = uuid.NullUUID{UUID: *f.TaskID, Valid: true}
	}
	if f.Cursor != "" {
		at, id, err := decodeKeysetCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: id, Valid: true}
	}

	q := store.New(s.pool)
	rows, err := q.ListRunsPage(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("runs: list runs: %w", err)
	}
	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1].Run
		next = encodeKeysetCursor(last.CreatedAt, last.ID)
	}
	if len(rows) == 0 {
		return []RunSummary{}, next, nil
	}

	runIDs := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		runIDs = append(runIDs, r.Run.ID)
	}
	phaseRows, err := q.ListLatestPhasesForRuns(ctx, runIDs)
	if err != nil {
		return nil, "", fmt.Errorf("runs: list page phases: %w", err)
	}
	live := make(map[uuid.UUID]map[string]PhaseSummary, len(rows))
	for _, pr := range phaseRows {
		byNode, ok := live[pr.RunID]
		if !ok {
			byNode = map[string]PhaseSummary{}
			live[pr.RunID] = byNode
		}
		status := PhaseStatus(pr.Status)
		byNode[pr.NodeID] = PhaseSummary{
			NodeID: pr.NodeID, Status: &status, Attempt: pr.Attempt, Generation: pr.Generation,
		}
	}

	// One compiled plan per DISTINCT workflow version on the page — the same
	// memo shape ListWaitingGates uses, and for the same reason: a list is
	// usually many runs over one or two workflows.
	plans := map[uuid.UUID]ExecPlan{}
	compiled := func(versionID uuid.UUID) (ExecPlan, error) {
		if p, ok := plans[versionID]; ok {
			return p, nil
		}
		def, err := s.defs.DefinitionByVersionID(ctx, versionID)
		if err != nil {
			return ExecPlan{}, err
		}
		p, err := compilePinned(def)
		if err != nil {
			return ExecPlan{}, err
		}
		plans[versionID] = p
		return p, nil
	}

	var projectIDs, repoIDs, versionIDs idSet
	for _, r := range rows {
		projectIDs.add(r.ProjectID)
		repoIDs.add(r.RepoID)
		versionIDs.add(r.WorkflowVersionID)
	}
	projectNames, err := s.params.Labels.ProjectNames(ctx, projectIDs.ids)
	if err != nil {
		return nil, "", fmt.Errorf("runs: resolve project labels: %w", err)
	}
	repoSlugs, err := s.params.Labels.RepoSlugs(ctx, repoIDs.ids)
	if err != nil {
		return nil, "", fmt.Errorf("runs: resolve repo labels: %w", err)
	}
	workflowNames, err := s.params.Labels.WorkflowNames(ctx, versionIDs.ids)
	if err != nil {
		return nil, "", fmt.Errorf("runs: resolve workflow labels: %w", err)
	}

	out := make([]RunSummary, 0, len(rows))
	for _, r := range rows {
		plan, err := compiled(r.WorkflowVersionID)
		if err != nil {
			return nil, "", err
		}
		byNode := live[r.Run.ID]
		summary := RunSummary{
			ID: r.Run.ID, TaskID: r.Run.TaskID, TaskTitle: r.Title,
			ProjectID: r.ProjectID, ProjectName: projectNames[r.ProjectID],
			RepoSlug: repoSlugs[r.RepoID], WorkflowName: workflowNames[r.WorkflowVersionID],
			Status: RunStatus(r.Run.Status), Generation: r.Run.Generation,
			RequesterID: r.RequesterID, CreatedAt: r.Run.CreatedAt,
			StartedAt: r.Run.StartedAt, FinishedAt: r.Run.FinishedAt,
			Phases: phaseSummaries(plan, byNode),
		}
		// The run's attempt is the newest attempt ordinal among its live phase
		// rows: a run has none of its own, and 0 is the honest answer for a run
		// nothing has materialized yet.
		for _, p := range byNode {
			if p.Attempt > summary.Attempt {
				summary.Attempt = p.Attempt
			}
		}
		out = append(out, summary)
	}
	return out, next, nil
}

// idSet collects distinct ids in first-seen order. The label port is batched by
// construction, so what it must never be handed is one entry per ROW.
type idSet struct {
	seen map[uuid.UUID]bool
	ids  []uuid.UUID
}

func (s *idSet) add(id uuid.UUID) {
	if s.seen == nil {
		s.seen = map[uuid.UUID]bool{}
	}
	if s.seen[id] {
		return
	}
	s.seen[id] = true
	s.ids = append(s.ids, id)
}
