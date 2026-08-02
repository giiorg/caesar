package identity_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/identity"
)

func TestGrantLevel_AtLeast_IsTheInclusiveLadder(t *testing.T) {
	tests := []struct {
		have, need identity.GrantLevel
		want       bool
	}{
		{identity.GrantView, identity.GrantView, true},
		{identity.GrantRun, identity.GrantView, true},
		{identity.GrantApprove, identity.GrantRun, true},
		{identity.GrantManage, identity.GrantApprove, true},
		{identity.GrantView, identity.GrantRun, false},
		{identity.GrantRun, identity.GrantApprove, false},
		{identity.GrantApprove, identity.GrantManage, false},
	}
	for _, tt := range tests {
		if got := tt.have.AtLeast(tt.need); got != tt.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", tt.have, tt.need, got, tt.want)
		}
	}
}

func TestGrantLevel_Valid(t *testing.T) {
	for _, ok := range []identity.GrantLevel{identity.GrantView, identity.GrantRun, identity.GrantApprove, identity.GrantManage} {
		if !ok.Valid() {
			t.Errorf("%s should be valid", ok)
		}
	}
	if identity.GrantLevel("owner").Valid() {
		t.Error(`"owner" should be invalid`)
	}
}

func TestRole_Valid(t *testing.T) {
	if !identity.RoleAdmin.Valid() || !identity.RoleMember.Valid() {
		t.Error("admin and member are the V1 roles")
	}
	if identity.Role("root").Valid() {
		t.Error(`"root" should be invalid`)
	}
}
