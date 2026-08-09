package workflows

import (
	"errors"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// Plan 04 hardening item 2: the message said "characters" while the check
// counts bytes, which is a lie for any non-ASCII name.
func TestWorkflowName_LengthErrorSaysBytes(t *testing.T) {
	_, err := validateWorkflowName(strings.Repeat("é", 200))
	var xe *xerrors.Error
	if !errors.As(err, &xe) {
		t.Fatal("want a classified error carrying field errors")
	}
	if len(xe.Fields) == 0 || xe.Fields[0].Field != "name" {
		t.Fatalf("fields = %+v, want one blaming \"name\"", xe.Fields)
	}
	if !strings.Contains(xe.Fields[0].Message, "bytes") {
		t.Errorf("message = %q; the check counts bytes and must say so", xe.Fields[0].Message)
	}
}
