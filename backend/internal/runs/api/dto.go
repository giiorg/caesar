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

// RunResponse carries phases and artifact metadata inline on the
// single-run read; list entries leave both empty.
type RunResponse struct {
	ID              string              `json:"id"`
	TaskID          string              `json:"task_id"`
	Status          string              `json:"status"`
	DefinitionHash  string              `json:"definition_hash"`
	BaseSHA         string              `json:"base_sha"`
	PolicySnapshot  runs.PolicySnapshot `json:"policy_snapshot"`
	ExecutionUsedMS int64               `json:"execution_used_ms"`
	WallDeadline    time.Time           `json:"wall_deadline"`
	Outcome         *OutcomeResponse    `json:"outcome"`
	CreatedAt       time.Time           `json:"created_at"`
	StartedAt       *time.Time          `json:"started_at"`
	FinishedAt      *time.Time          `json:"finished_at"`
	Phases          []PhaseResponse     `json:"phases"`
	Artifacts       []ArtifactResponse  `json:"artifacts"`
}

type RunListResponse struct {
	Runs []RunResponse `json:"runs"`
}

type OutcomeResponse struct {
	Class   string `json:"class"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PhaseResponse struct {
	NodeID      string           `json:"node_id"`
	Attempt     int32            `json:"attempt"`
	MaxAttempts int32            `json:"max_attempts"`
	Status      string           `json:"status"`
	Outcome     *OutcomeResponse `json:"outcome"`
	CreatedAt   time.Time        `json:"created_at"`
	FinishedAt  *time.Time       `json:"finished_at"`
}

type ArtifactResponse struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	Attempt   int32     `json:"attempt"`
	Type      string    `json:"type"`
	ParentIDs []string  `json:"parent_ids"`
	CreatedAt time.Time `json:"created_at"`
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

func runResponse(r runs.Run, phases []runs.Phase, arts []runs.ArtifactMeta) RunResponse {
	out := RunResponse{
		ID: r.ID.String(), TaskID: r.TaskID.String(), Status: string(r.Status),
		DefinitionHash: r.DefinitionHash, BaseSHA: r.BaseSHA, PolicySnapshot: r.Snapshot,
		ExecutionUsedMS: r.ExecutionUsedMS, WallDeadline: r.WallDeadline,
		Outcome: outcomeResponse(r.Outcome), CreatedAt: r.CreatedAt,
		StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
		Phases: make([]PhaseResponse, 0, len(phases)), Artifacts: make([]ArtifactResponse, 0, len(arts)),
	}
	for _, p := range phases {
		out.Phases = append(out.Phases, PhaseResponse{
			NodeID: p.NodeID, Attempt: p.Attempt, MaxAttempts: p.MaxAttempts, Status: string(p.Status),
			Outcome: outcomeResponse(p.Outcome), CreatedAt: p.CreatedAt, FinishedAt: p.FinishedAt,
		})
	}
	for _, a := range arts {
		parents := make([]string, 0, len(a.ParentIDs))
		for _, id := range a.ParentIDs {
			parents = append(parents, id.String())
		}
		out.Artifacts = append(out.Artifacts, ArtifactResponse{
			ID: a.ID.String(), NodeID: a.NodeID, Attempt: a.Attempt, Type: a.Type,
			ParentIDs: parents, CreatedAt: a.CreatedAt,
		})
	}
	return out
}

func outcomeResponse(o *runs.Outcome) *OutcomeResponse {
	if o == nil {
		return nil
	}
	return &OutcomeResponse{Class: string(o.Class), Code: o.Code, Message: o.Message}
}

func eventResponse(e runs.RunEventRecord) RunEventResponse {
	return RunEventResponse{ID: e.ID, Kind: e.Kind, Payload: e.Payload, OccurredAt: e.OccurredAt}
}
