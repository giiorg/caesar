package identity

import "testing"

func TestGrantLevel_AtLeast_IsTheInclusiveLadder(t *testing.T) {
	tests := []struct {
		have, need GrantLevel
		want       bool
	}{
		{GrantView, GrantView, true},
		{GrantRun, GrantView, true},
		{GrantApprove, GrantRun, true},
		{GrantManage, GrantApprove, true},
		{GrantView, GrantRun, false},
		{GrantRun, GrantApprove, false},
		{GrantApprove, GrantManage, false},
	}
	for _, tt := range tests {
		if got := tt.have.AtLeast(tt.need); got != tt.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", tt.have, tt.need, got, tt.want)
		}
	}
}

func TestGrantLevel_Valid(t *testing.T) {
	for _, ok := range []GrantLevel{GrantView, GrantRun, GrantApprove, GrantManage} {
		if !ok.Valid() {
			t.Errorf("%s should be valid", ok)
		}
	}
	if GrantLevel("owner").Valid() {
		t.Error(`"owner" should be invalid`)
	}
}

func TestRole_Valid(t *testing.T) {
	if !RoleAdmin.Valid() || !RoleMember.Valid() {
		t.Error("admin and member are the V1 roles")
	}
	if Role("root").Valid() {
		t.Error(`"root" should be invalid`)
	}
}
