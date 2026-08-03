// Package identity is the identity & access bounded context (spec §2, §6):
// users, sessions, the grant ladder, login. Peers never import it except
// through app wiring; it owns its transactions end to end.
package identity

import (
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
)

// The RBAC vocabulary moved to platform/authn in Plan 03 (shared kernel)
// so peer contexts can consume it. Aliases keep identity's API unchanged.
type (
	Role        = authn.Role
	GrantLevel  = authn.GrantLevel
	Principal   = authn.Principal
	AuthSession = authn.AuthSession
	Grant       = authn.Grant
)

const (
	RoleAdmin  = authn.RoleAdmin
	RoleMember = authn.RoleMember

	GrantView    = authn.GrantView
	GrantRun     = authn.GrantRun
	GrantApprove = authn.GrantApprove
	GrantManage  = authn.GrantManage
)

// User is the domain view of a user row (never exposes the hash).
type User struct {
	ID        uuid.UUID
	Email     string
	Role      Role
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}
