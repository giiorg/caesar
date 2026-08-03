// Package identity is the identity & access bounded context (spec §2, §6):
// users, sessions, the grant ladder, login. Peers never import it except
// through app wiring; it owns its transactions end to end.
package identity

import (
	"time"

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

// AuthSession is a principal plus the session that authenticated it —
// what logout and rotation operate on.
type AuthSession struct {
	Principal
	SessionID uuid.UUID
}

// User is the domain view of a user row (never exposes the hash).
type User struct {
	ID        uuid.UUID
	Email     string
	Role      Role
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Grant is one rung of the ladder on one project.
type Grant struct {
	ProjectID uuid.UUID
	Level     GrantLevel
}
