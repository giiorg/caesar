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

// WorkflowVersionResponse is the full version, document included, and every
// field of it is required — `versionResponse` below sets all five
// unconditionally and none of them can be absent.
//
// DOCUMENT'S MARK IS THE ONE THAT MATTERS and the list projection is not an
// argument against it: the list uses WorkflowVersionMetaResponse, a separate
// type with no Document field at all, so there is no shape in which a
// WorkflowVersionResponse lacks its document. `omitempty` had to go with it —
// the pairing is a contradiction, the same one Task 17 removed from LastUsedAt.
//
// THE $ref RULE DOES NOT REACH THIS FIELD, and not merely because
// json.RawMessage is no pointer-to-named-struct: swaggest reflects it to the
// EMPTY schema (`"document": {}` — any JSON), so no $ref is emitted for the
// property in the first place and there is no null branch for `required` to
// contradict. It reaches the client as `unknown`, which is the honest type for
// a blob whose shape the write door validates and the transport contract does
// not: web narrows it with readers that answer null, never with a cast.
type WorkflowVersionResponse struct {
	WorkflowID  string          `json:"workflow_id" required:"true"`
	Version     int             `json:"version" required:"true"`
	ContentHash string          `json:"content_hash" required:"true"`
	Document    json.RawMessage `json:"document" required:"true"`
	CreatedAt   time.Time       `json:"created_at" required:"true"`
}

type WorkflowVersionMetaResponse struct {
	Version     int       `json:"version" required:"true"`
	ContentHash string    `json:"content_hash" required:"true"`
	CreatedAt   time.Time `json:"created_at" required:"true"`
}

type WorkflowVersionListResponse struct {
	Versions []WorkflowVersionMetaResponse `json:"versions" required:"true"`
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
