// Package api is the execution spine's HTTP surface. DTO structs here
// are the source the OpenAPI document is generated from (Go types →
// contract, spec §4).
package api

import (
	"time"

	"github.com/giiorg/caesar/backend/internal/runs"
)

type CreateTaskRequest struct {
	RepoID          string         `json:"repo_id"`
	WorkflowID      string         `json:"workflow_id"`
	WorkflowVersion int32          `json:"workflow_version"`
	Title           string         `json:"title"`
	Inputs          map[string]any `json:"inputs,omitempty"`
}

// TaskResponse projects a task with its newest run: Status is that run's
// status, which is what a task list renders.
type TaskResponse struct {
	ID                string         `json:"id"`
	ProjectID         string         `json:"project_id"`
	RepoID            string         `json:"repo_id"`
	WorkflowVersionID string         `json:"workflow_version_id"`
	Title             string         `json:"title"`
	Inputs            map[string]any `json:"inputs"`
	RequesterID       string         `json:"requester_id"`
	Status            string         `json:"status"`
	LatestRunID       string         `json:"latest_run_id"`
	CreatedAt         time.Time      `json:"created_at"`
}

type TaskListResponse struct {
	Tasks []TaskResponse `json:"tasks"`
}

// RunResponse carries phases, artifact metadata and publications inline on
// the single-run read; list entries leave all three empty.
type RunResponse struct {
	ID             string              `json:"id"`
	TaskID         string              `json:"task_id"`
	Status         string              `json:"status"`
	DefinitionHash string              `json:"definition_hash"`
	BaseSHA        string              `json:"base_sha"`
	PolicySnapshot runs.PolicySnapshot `json:"policy_snapshot"`
	// Generation is the revision round the run is on: 1 until a gate sends
	// work back, one higher per round after that.
	Generation      int32     `json:"generation"`
	ExecutionUsedMS int64     `json:"execution_used_ms"`
	WallDeadline    time.Time `json:"wall_deadline"`
	// Outcome.Class on a terminal run can still read "transient" when the run
	// died parked past its deadline over a settled publication. That is the
	// orchestrator's classification, not this projection's — it predates the
	// gate surface and is routed to Task 14; nothing here makes it worse.
	Outcome      *OutcomeResponse      `json:"outcome"`
	CreatedAt    time.Time             `json:"created_at"`
	StartedAt    *time.Time            `json:"started_at"`
	FinishedAt   *time.Time            `json:"finished_at"`
	Phases       []PhaseResponse       `json:"phases"`
	Artifacts    []ArtifactResponse    `json:"artifacts"`
	Publications []PublicationResponse `json:"publications"`
}

type RunListResponse struct {
	Runs []RunResponse `json:"runs"`
}

type OutcomeResponse struct {
	Class   string     `json:"class"`
	Code    string     `json:"code"`
	Message string     `json:"message"`
	RetryAt *time.Time `json:"retry_at,omitempty"`
}

type PhaseResponse struct {
	NodeID      string           `json:"node_id"`
	Attempt     int32            `json:"attempt"`
	MaxAttempts int32            `json:"max_attempts"`
	Status      string           `json:"status"`
	Generation  int32            `json:"generation"`
	Outcome     *OutcomeResponse `json:"outcome"`
	CreatedAt   time.Time        `json:"created_at"`
	FinishedAt  *time.Time       `json:"finished_at"`
	// Decision is present only on a gate attempt somebody decided. A revision
	// leaves two gate attempts on the same node id, each carrying its own.
	Decision *GateDecisionSummary `json:"decision"`
}

type ArtifactResponse struct {
	ID         string    `json:"id"`
	NodeID     string    `json:"node_id"`
	Attempt    int32     `json:"attempt"`
	Generation int32     `json:"generation"`
	Type       string    `json:"type"`
	ParentIDs  []string  `json:"parent_ids"`
	CreatedAt  time.Time `json:"created_at"`
}

type GateDecisionRequest struct {
	Decision string `json:"decision"` // approve | request_changes | reject
	Note     string `json:"note,omitempty"`
}

type GateDecisionResponse struct {
	DecisionID string `json:"decision_id"`
	Decision   string `json:"decision"`
	RunStatus  string `json:"run_status"`
	// Generation is the generation this response is about, and which one that
	// is depends on the decision: approve and reject report the generation you
	// just DECIDED, request_changes reports the NEW generation your revision
	// opened. One documented field beats two where one is always null.
	Generation int32 `json:"generation"`
	// AuthorizationID is empty when the gate authorizes no publish node.
	AuthorizationID string `json:"authorization_id,omitempty"`
}

// ApprovalResponse is one attention item. It carries everything a list view
// renders so the inbox is one request, not one plus N.
//
// required:"true" is read by swaggest/jsonschema-go and puts the property in
// the schema's `required` array; without it openapi-typescript renders it
// optional. Only Plan 11's web slice is marked up — the rest of this file's
// DTOs are Plan 12's to do as those screens arrive.
type ApprovalResponse struct {
	RunID        string    `json:"run_id" required:"true"`
	TaskID       string    `json:"task_id" required:"true"`
	ProjectID    string    `json:"project_id" required:"true"`
	TaskTitle    string    `json:"task_title" required:"true"`
	NodeID       string    `json:"node_id" required:"true"`
	Attempt      int32     `json:"attempt" required:"true"`
	Generation   int32     `json:"generation" required:"true"`
	RequesterID  string    `json:"requester_id" required:"true"`
	WaitingSince time.Time `json:"waiting_since" required:"true"`
}

// Approvals is required AND nullable: the property is always present in the
// contract, and its type stays ["array","null"] because a nil slice really
// does marshal to null. The two are independent.
type ApprovalListResponse struct {
	Approvals []ApprovalResponse `json:"approvals" required:"true"`
}

// GateDecisionSummary projects a decided gate onto its phase. The approver's
// id and address are separate fields: a run is readable with view, so the
// address reaches every member of the project and that has to be visible in
// the contract rather than hidden inside one ambiguous "acted_by".
type GateDecisionSummary struct {
	Decision           string    `json:"decision"`
	Note               string    `json:"note"`
	ActedByID          string    `json:"acted_by_id"`
	ActedByEmail       string    `json:"acted_by_email"`
	ActedAt            time.Time `json:"acted_at"`
	SelfApproval       bool      `json:"self_approval"`
	ExaminedArtifactID string    `json:"examined_artifact_id"`
}

// PublicationResponse projects one publish intent. absent_since is
// deliberately absent: it is the settlement sweep's own bookkeeping, it moves
// on every tick, and rendering it would invite a human to act on a probe that
// is designed to be re-run.
type PublicationResponse struct {
	NodeID       string `json:"node_id"`
	Status       string `json:"status"` // pending | pushed | published | halted
	Branch       string `json:"branch"`
	TargetBranch string `json:"target_branch"`
	BaseSHA      string `json:"base_sha"`
	HeadSHA      string `json:"head_sha,omitempty"`
	PRNumber     int64  `json:"pr_number,omitempty"`
	PRURL        string `json:"pr_url,omitempty"`
	HaltReason   string `json:"halt_reason,omitempty"`
}

type RunEventsResponse struct {
	Events []RunEventResponse `json:"events"`
}

type RunEventResponse struct {
	ID         int64          `json:"id"`
	Kind       string         `json:"kind"`
	Payload    map[string]any `json:"payload"`
	OccurredAt time.Time      `json:"occurred_at"`
}

func taskResponse(t runs.Task) TaskResponse {
	return TaskResponse{
		ID: t.ID.String(), ProjectID: t.ProjectID.String(), RepoID: t.RepoID.String(),
		WorkflowVersionID: t.WorkflowVersionID.String(), Title: t.Title, Inputs: t.Inputs,
		RequesterID: t.RequesterID.String(), Status: string(t.LatestRunStatus),
		LatestRunID: t.LatestRunID.String(), CreatedAt: t.CreatedAt,
	}
}

func runResponse(d runs.RunDetail) RunResponse {
	r := d.Run
	out := RunResponse{
		ID: r.ID.String(), TaskID: r.TaskID.String(), Status: string(r.Status),
		DefinitionHash: r.DefinitionHash, BaseSHA: r.BaseSHA, PolicySnapshot: r.Snapshot,
		Generation:      r.Generation,
		ExecutionUsedMS: r.ExecutionUsedMS, WallDeadline: r.WallDeadline,
		Outcome: outcomeResponse(r.Outcome), CreatedAt: r.CreatedAt,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Phases:       make([]PhaseResponse, 0, len(d.Phases)),
		Artifacts:    make([]ArtifactResponse, 0, len(d.Artifacts)),
		Publications: make([]PublicationResponse, 0, len(d.Publications)),
	}
	for _, p := range d.Phases {
		out.Phases = append(out.Phases, PhaseResponse{
			NodeID: p.NodeID, Attempt: p.Attempt, MaxAttempts: p.MaxAttempts, Status: string(p.Status),
			Generation: p.Generation, Outcome: outcomeResponse(p.Outcome),
			CreatedAt: p.CreatedAt, FinishedAt: p.FinishedAt, Decision: gateDecisionSummary(p.Decision),
		})
	}
	for _, a := range d.Artifacts {
		parents := make([]string, 0, len(a.ParentIDs))
		for _, id := range a.ParentIDs {
			parents = append(parents, id.String())
		}
		out.Artifacts = append(out.Artifacts, ArtifactResponse{
			ID: a.ID.String(), NodeID: a.NodeID, Attempt: a.Attempt, Generation: a.Generation,
			Type: a.Type, ParentIDs: parents, CreatedAt: a.CreatedAt,
		})
	}
	for _, p := range d.Publications {
		out.Publications = append(out.Publications, PublicationResponse{
			NodeID: p.NodeID, Status: p.Status, Branch: p.Branch, TargetBranch: p.TargetBranch,
			BaseSHA: p.BaseSHA, HeadSHA: p.HeadSHA, PRNumber: p.PRNumber, PRURL: p.PRURL,
			HaltReason: p.HaltReason,
		})
	}
	return out
}

func gateDecisionSummary(d *runs.GateDecision) *GateDecisionSummary {
	if d == nil {
		return nil
	}
	return &GateDecisionSummary{
		Decision: d.Decision, Note: d.Note, ActedByID: d.ActedByID.String(),
		ActedByEmail: d.ActedByEmail, ActedAt: d.ActedAt, SelfApproval: d.SelfApproval,
		ExaminedArtifactID: d.ExaminedArtifactID.String(),
	}
}

func approvalResponse(g runs.WaitingGate) ApprovalResponse {
	return ApprovalResponse{
		RunID: g.RunID.String(), TaskID: g.TaskID.String(), ProjectID: g.ProjectID.String(),
		TaskTitle: g.TaskTitle, NodeID: g.NodeID, Attempt: g.Attempt, Generation: g.Generation,
		RequesterID: g.RequesterID.String(), WaitingSince: g.WaitingSince,
	}
}

func outcomeResponse(o *runs.Outcome) *OutcomeResponse {
	if o == nil {
		return nil
	}
	return &OutcomeResponse{Class: string(o.Class), Code: o.Code, Message: o.Message, RetryAt: o.RetryAt}
}

func eventResponse(e runs.RunEventRecord) RunEventResponse {
	return RunEventResponse{ID: e.ID, Kind: e.Kind, Payload: e.Payload, OccurredAt: e.OccurredAt}
}
