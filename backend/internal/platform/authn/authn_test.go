package authn_test

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
)

func TestGrantLevel_LadderIsInclusive(t *testing.T) {
	cases := []struct {
		have, min authn.GrantLevel
		want      bool
	}{
		{authn.GrantView, authn.GrantView, true},
		{authn.GrantView, authn.GrantRun, false},
		{authn.GrantRun, authn.GrantView, true},
		{authn.GrantApprove, authn.GrantRun, true},
		{authn.GrantManage, authn.GrantApprove, true},
		{authn.GrantManage, authn.GrantManage, true},
		{"", authn.GrantView, false},
		{authn.GrantView, "", false},
		{"bogus", authn.GrantView, false},
	}
	for _, c := range cases {
		if got := c.have.AtLeast(c.min); got != c.want {
			t.Errorf("%q.AtLeast(%q) = %v, want %v", c.have, c.min, got, c.want)
		}
	}
}

func TestRoleAndGrantLevel_Validity(t *testing.T) {
	if !authn.RoleAdmin.Valid() || !authn.RoleMember.Valid() || authn.Role("root").Valid() {
		t.Fatal("role validity wrong")
	}
	if !authn.GrantManage.Valid() || authn.GrantLevel("owner").Valid() {
		t.Fatal("grant level validity wrong")
	}
}
