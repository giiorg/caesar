package httpx_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestLastEventID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		header  string
		set     bool
		wantID  int64
		wantHas bool
		wantErr bool
	}{
		{name: "absent is not a resume"},
		{name: "empty is not a resume", header: "", set: true},
		{name: "an id resumes", header: "42", set: true, wantID: 42, wantHas: true},
		// 0 is a legitimate value, not a sentinel: the caller distinguishes
		// "resume from 0" from "no resume" by the second return, which is the
		// whole reason it exists (realtime.StreamRequest.HasLastID).
		{name: "zero is a resume", header: "0", set: true, wantID: 0, wantHas: true},
		{name: "junk is rejected", header: "soon", set: true, wantErr: true},
		{name: "negative is rejected", header: "-1", set: true, wantErr: true},
		{name: "float is rejected", header: "1.5", set: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil)
			if tc.set {
				r.Header.Set("Last-Event-ID", tc.header)
			}
			id, has, err := httpx.LastEventID(r)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LastEventID(%q) error = nil, want one", tc.header)
				}
				if got := xerrors.ClassOf(err); got != xerrors.ClassInvalid {
					t.Errorf("class = %v, want %v", got, xerrors.ClassInvalid)
				}
				var xe *xerrors.Error
				if !errors.As(err, &xe) || len(xe.Fields) != 1 || xe.Fields[0].Field != "Last-Event-ID" {
					t.Errorf("fields = %+v, want one blaming Last-Event-ID", xe)
				}
				return
			}
			if err != nil {
				t.Fatalf("LastEventID(%q) error = %v, want nil", tc.header, err)
			}
			if id != tc.wantID || has != tc.wantHas {
				t.Errorf("LastEventID(%q) = (%d, %v), want (%d, %v)", tc.header, id, has, tc.wantID, tc.wantHas)
			}
		})
	}
}
