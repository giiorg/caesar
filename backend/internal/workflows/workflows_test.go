package workflows

import (
	"errors"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// Plan 04 hardening item 2: the message said "characters" while the check
// counts bytes, which is a lie for any non-ASCII name.
//
// Both validators are here because the original fix took the name and left
// the description ELEVEN LINES BELOW IT in the same file — the one shape a
// single-function test cannot catch. Each case is refused for a reason the
// wording alone cannot fake: the input is under the limit in runes and over
// it in bytes, so a "fix" that switched to utf8.RuneCountInString would
// accept it and fail here, and one that only reworded would pass here and
// fail the wording assertion. The unit is the assertion, not the prose.
func TestWorkflowValidation_LengthLimitsAreBytesAndSaySo(t *testing.T) {
	cases := []struct {
		name  string
		field string
		err   error
	}{
		// 200 runes, 400 bytes, against a 100-byte limit.
		{"name", "name", func() error { _, e := validateWorkflowName(strings.Repeat("é", 200)); return e }()},
		// 300 runes, 600 bytes, against a 500-byte limit.
		{"description", "description", validateWorkflowDescription(strings.Repeat("é", 300))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var xe *xerrors.Error
			if !errors.As(c.err, &xe) {
				t.Fatalf("err = %v; want a classified error carrying field errors — the input is "+
					"over the limit in BYTES, which is what the check counts", c.err)
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
