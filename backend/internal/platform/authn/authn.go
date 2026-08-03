// Package authn is the shared-kernel RBAC vocabulary (spec §6): roles, the
// per-project grant ladder, and the requester principal that rides the
// request context into every domain write (spec §4). Promoted out of
// identity in Plan 03 so peer contexts can authorize without importing it;
// identity keeps aliases and identity.Service implements both ports.
package authn

import (
	"context"

	"github.com/google/uuid"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

func (r Role) Valid() bool { return r == RoleAdmin || r == RoleMember }

// GrantLevel is the per-project inclusive ladder (spec §6):
// view ⊂ run ⊂ approve ⊂ manage. Checks are always AtLeast.
type GrantLevel string

const (
	GrantView    GrantLevel = "view"
	GrantRun     GrantLevel = "run"
	GrantApprove GrantLevel = "approve"
	GrantManage  GrantLevel = "manage"
)

var grantRank = map[GrantLevel]int{GrantView: 1, GrantRun: 2, GrantApprove: 3, GrantManage: 4}

func (g GrantLevel) Valid() bool { return grantRank[g] != 0 }

func (g GrantLevel) AtLeast(min GrantLevel) bool {
	return grantRank[g] != 0 && grantRank[min] != 0 && grantRank[g] >= grantRank[min]
}

// Principal is the authenticated human riding the request context — the
// requester principal of spec §6.
type Principal struct {
	UserID uuid.UUID
	Email  string
	Role   Role
}

// AuthSession is a principal plus the session that authenticated it.
type AuthSession struct {
	Principal
	SessionID uuid.UUID
}

// Grant is one rung of the ladder on one project.
type Grant struct {
	ProjectID uuid.UUID
	Level     GrantLevel
}

// SessionCookie is the pinned __Host- session cookie name (spec §6),
// shared by every API package that authenticates requests.
const SessionCookie = "__Host-caesar_session"

// SessionAuthenticator resolves a session token to its principal.
// identity.Service is the sole implementation.
type SessionAuthenticator interface {
	AuthenticateSession(ctx context.Context, token string) (AuthSession, error)
}

// Authorizer is the single RBAC check: global admin passes everything;
// members need a grant at or above min on the project.
// identity.Service is the sole implementation.
type Authorizer interface {
	Authorize(ctx context.Context, p Principal, projectID uuid.UUID, min GrantLevel) (bool, error)
}
