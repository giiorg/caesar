//go:build !embedui

package webui_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/webui"
)

// TestTaglessTwinRefusesWithTypedProblemJSON pins the tagless twin exactly.
//
// It exists because ui_test.go cannot: that file accepts EITHER 200 or 501, so
// it is satisfied by a tagless binary that has quietly started answering 200 —
// which is precisely the "a stub masquerades as a working UI" failure the build
// tag was introduced to make impossible. A guard that accepts both twins'
// answers cannot detect one twin turning into the other.
func TestTaglessTwinRefusesWithTypedProblemJSON(t *testing.T) {
	for _, path := range []string{"/", "/approvals", "/assets/index-abc.js"} {
		rec := httptest.NewRecorder()
		webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("GET %s = %d, want 501 — a tagless binary must never answer as if a UI were embedded", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("GET %s Content-Type = %q, want application/problem+json", path, ct)
		}
		var pd struct {
			Type   string `json:"type"`
			Title  string `json:"title"`
			Status int    `json:"status"`
			Detail string `json:"detail"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&pd); err != nil {
			t.Fatalf("GET %s body is not JSON: %v", path, err)
		}
		if pd.Status != http.StatusNotImplemented {
			t.Errorf("problem.status = %d, want 501 (it must agree with the HTTP status)", pd.Status)
		}
		if pd.Type == "" || pd.Title == "" {
			t.Errorf("GET %s problem envelope is not filled in: %+v", path, pd)
		}
		// The detail says WHY, by name. An operator staring at a 501 needs the
		// word "embedui" to know the fix is a build flag, not a config key.
		//
		// "not empty" is NOT enough, and that is measured rather than
		// hypothetical: routing this through httpx.Problem with
		// xerrors.NotImplemented — which is what it looks like it should do —
		// yields the redacted detail "internal error", a perfectly non-empty
		// string that tells the operator nothing. xerrors.NotImplemented
		// carries no class, so ClassOf reports ClassInternal and the detail is
		// dropped by design (default-deny). This assertion is what forces the
		// envelope to be built explicitly.
		if !strings.Contains(pd.Detail, "embedui") {
			t.Errorf("GET %s problem detail = %q, want it to name the embedui build tag", path, pd.Detail)
		}
	}
}
