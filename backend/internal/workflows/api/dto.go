// Package api is workflows' HTTP surface. DTO structs here are the
// source the OpenAPI document is generated from (Go types → contract,
// spec §4). The definition document itself rides as json.RawMessage —
// its schema is enforced by the write door, not the transport contract.
package api

import (
	"encoding/json"
	"time"

	"github.com/giiorg/caesar/backend/internal/workflows"
)

// WorkflowResponse is required throughout, and LatestVersion is the reason it
// had to be: the new-task form submits `workflow_version: latest_version`, and
// an optional property there would make the client branch on an absence
// `workflowResponse` cannot produce — it sets all six unconditionally and none
// carries omitempty.
type WorkflowResponse struct {
	ID            string    `json:"id" required:"true"`
	Name          string    `json:"name" required:"true"`
	Description   string    `json:"description" required:"true"`
	LatestVersion int       `json:"latest_version" required:"true"`
	CreatedAt     time.Time `json:"created_at" required:"true"`
	UpdatedAt     time.Time `json:"updated_at" required:"true"`
}

type WorkflowListResponse struct {
	Workflows []WorkflowResponse `json:"workflows" required:"true"`
}

type CreateWorkflowRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type UpdateWorkflowRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

type CreateWorkflowVersionRequest struct {
	Document json.RawMessage `json:"document"`
}

type WorkflowVersionResponse struct {
	WorkflowID  string          `json:"workflow_id"`
	Version     int             `json:"version"`
	ContentHash string          `json:"content_hash"`
	Document    json.RawMessage `json:"document,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
}

type WorkflowVersionMetaResponse struct {
	Version     int       `json:"version"`
	ContentHash string    `json:"content_hash"`
	CreatedAt   time.Time `json:"created_at"`
}

type WorkflowVersionListResponse struct {
	Versions []WorkflowVersionMetaResponse `json:"versions"`
}

func workflowResponse(w workflows.Workflow) WorkflowResponse {
	return WorkflowResponse{
		ID: w.ID.String(), Name: w.Name, Description: w.Description,
		LatestVersion: int(w.LatestVersion), CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

func versionResponse(v workflows.Version) WorkflowVersionResponse {
	return WorkflowVersionResponse{
		WorkflowID: v.WorkflowID.String(), Version: int(v.Version), ContentHash: v.ContentHash,
		Document: json.RawMessage(v.Document), CreatedAt: v.CreatedAt,
	}
}
