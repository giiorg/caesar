// Package api is identity's HTTP surface. DTO structs here are the source
// the OpenAPI document is generated from (Go types → contract, spec §4).
package api

import (
	"time"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
)

// required:"true" on the fields below is read by swaggest/jsonschema-go and
// puts the property in the schema's `required` array; without it
// openapi-typescript renders every property optional. Every RESPONSE type
// here is now marked — Plan 11 did the login/me slice, Plan 12 the
// admin-users and project-grants ones as their screens arrived. The request
// bodies are not swept: LoginRequest is marked because Plan 11's form read
// it, and UpdateUserRequest must STAY unmarked because a PATCH that omits a
// key means "leave it alone". CreateUserRequest, ChangePasswordRequest and
// SetGrantRequest are simply unswept — they are inputs, so no screen reads
// them through a generated type. required and nullable are independent:
// Grants stays type ["array","null"] because the handler really can emit
// null.

type LoginRequest struct {
	Email    string `json:"email" required:"true"`
	Password string `json:"password" required:"true"`
}

type UserResponse struct {
	ID        string    `json:"id" required:"true"`
	Email     string    `json:"email" required:"true"`
	Role      string    `json:"role" required:"true"`
	Active    bool      `json:"active" required:"true"`
	CreatedAt time.Time `json:"created_at" required:"true"`
	UpdatedAt time.Time `json:"updated_at" required:"true"`
}

type GrantResponse struct {
	ProjectID string `json:"project_id" required:"true"`
	Level     string `json:"level" required:"true"`
}

type MeResponse struct {
	User   UserResponse    `json:"user" required:"true"`
	Grants []GrantResponse `json:"grants" required:"true"`
}

type UserListResponse struct {
	Users []UserResponse `json:"users" required:"true"`
}

type CreateUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

type UpdateUserRequest struct {
	Role   *string `json:"role,omitempty"`
	Active *bool   `json:"active,omitempty"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type ProjectGrantResponse struct {
	UserID string `json:"user_id" required:"true"`
	Email  string `json:"email" required:"true"`
	Level  string `json:"level" required:"true"`
}

type GrantListResponse struct {
	Grants []ProjectGrantResponse `json:"grants" required:"true"`
}

type SetGrantRequest struct {
	UserID string `json:"user_id"`
	Level  string `json:"level"`
}

// ListAuditQuery is the query string, declared as a struct so the parameters
// reach the contract. Every field is `query`-tagged, so the reflector renders
// parameters and no request body.
type ListAuditQuery struct {
	Cursor string `query:"cursor" description:"opaque; the previous page's next_cursor"`
	Limit  int32  `query:"limit" description:"1-200, default 50"`
}

// AuditEntryResponse is one audit row.
//
// ActorUserID is required AND nullable, which is the honest shape rather than
// an omission: a system action — bootstrap, an expiry sweep — genuinely has no
// actor, and a client that had to distinguish "absent key" from "null" would be
// reading the difference as a bug. Detail is required for the same reason as
// RunEventResponse.Payload: it is always marshaled, and a nil map is null.
type AuditEntryResponse struct {
	ID            int64          `json:"id" required:"true"`
	OccurredAt    time.Time      `json:"occurred_at" required:"true"`
	ActorUserID   *string        `json:"actor_user_id" required:"true"`
	Action        string         `json:"action" required:"true"`
	SubjectKind   string         `json:"subject_kind" required:"true"`
	SubjectID     string         `json:"subject_id" required:"true"`
	CorrelationID string         `json:"correlation_id" required:"true"`
	Detail        map[string]any `json:"detail" required:"true"`
}

// AuditPageResponse is one page. next_cursor is required AND nullable: null
// means "this was the last page", which is a value the client branches on, not
// a key it fails to find.
type AuditPageResponse struct {
	Entries    []AuditEntryResponse `json:"entries" required:"true"`
	NextCursor *string              `json:"next_cursor" required:"true"`
}

func userResponse(u identity.User) UserResponse {
	return UserResponse{
		ID: u.ID.String(), Email: u.Email, Role: string(u.Role), Active: u.Active,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

func auditPageResponse(p audit.Page) AuditPageResponse {
	out := AuditPageResponse{Entries: make([]AuditEntryResponse, 0, len(p.Entries))}
	if p.NextCursor != "" {
		out.NextCursor = &p.NextCursor
	}
	for _, e := range p.Entries {
		row := AuditEntryResponse{
			ID: e.ID, OccurredAt: e.OccurredAt, Action: e.Action,
			SubjectKind: e.SubjectKind, SubjectID: e.SubjectID,
			CorrelationID: e.CorrelationID, Detail: e.Detail,
		}
		if e.Actor.Valid {
			id := e.Actor.UUID.String()
			row.ActorUserID = &id
		}
		out.Entries = append(out.Entries, row)
	}
	return out
}
