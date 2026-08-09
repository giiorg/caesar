// In-package: the length validators are unexported and every exported route
// to them needs a database, so an external test would pay a container to
// assert a string constant.
package assets

import (
	"errors"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// The four length limits this package enforces are BYTE limits — `len()`, over
// columns that are unconstrained `text` — and their field errors used to say
// "characters", which is wrong for every non-ASCII input.
//
// Each input below is UNDER its limit in runes and OVER it in bytes, so the
// two halves are pinned separately and neither can be faked: a switch to
// utf8.RuneCountInString accepts the input and fails at the first assertion,
// and a reword-only change fails at the second.
func TestValidation_LengthLimitsAreBytesAndSaySo(t *testing.T) {
	// Every rune is 2 bytes, so each input is exactly twice its limit in bytes
	// and just past half of it in runes.
	cases := []struct {
		name  string
		field string
		err   error
	}{
		{"name", "project_name", func() error { _, e := validateName("project_name", strings.Repeat("é", maxNameLen)); return e }()},
		{"description", "description", validateDescription(strings.Repeat("é", maxDescriptionLen))},
		{"image ref", "image_ref", validateImageRef(strings.Repeat("é", maxImageRefLen))},
		{"branch", "default_branch", validateBranch(strings.Repeat("é", maxBranchLen))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var xe *xerrors.Error
			if !errors.As(c.err, &xe) {
				t.Fatalf("err = %v; want a refusal — the input is over the limit in BYTES, "+
					"which is the unit every check here counts in", c.err)
			}
			if len(xe.Fields) == 0 || xe.Fields[0].Field != c.field {
				t.Fatalf("fields = %+v, want one blaming %q", xe.Fields, c.field)
			}
			if !strings.Contains(xe.Fields[0].Message, "bytes") {
				t.Errorf("message = %q; the check counts bytes and must say so", xe.Fields[0].Message)
			}
		})
	}
}
