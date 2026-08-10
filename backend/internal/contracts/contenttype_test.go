package contracts

import (
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

func TestContentTypeOf_DefaultsToJSON(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		op   httpx.Operation
		want string
	}{
		{"unset means json", httpx.Operation{}, "application/json"},
		{"explicit stream", httpx.Operation{ResponseContentType: "text/event-stream"}, "text/event-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := contentTypeOf(tc.op); got != tc.want {
				t.Errorf("contentTypeOf() = %q, want %q", got, tc.want)
			}
		})
	}
}
