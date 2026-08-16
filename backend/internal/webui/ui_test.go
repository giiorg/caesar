package webui_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/giiorg/caesar/backend/internal/webui"
)

// TestHandlerIsAlwaysConstructible pins the property the build tag exists for:
// whichever twin compiled, there IS a handler and it answers. A tagless binary
// says out loud that no UI is embedded — it never serves a stub page that a
// forgotten `vite build` left behind, which is the failure mode a committed
// placeholder dist would have.
//
// This file carries no build tag, so it is the only assertion that runs in
// BOTH lanes — and it is correspondingly weak, because the two lanes disagree
// about everything except "there is a handler and it writes a typed response".
// Do not read it as the specification of either twin: ui_noembed_test.go pins
// the refusal exactly and ui_embed_test.go pins the served bundle exactly, and
// those two are where a regression in either twin actually gets caught.
func TestHandlerIsAlwaysConstructible(t *testing.T) {
	rec := httptest.NewRecorder()
	webui.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 200 (embedui) or 501 (tagless)", rec.Code)
	}
	// Neither twin may answer with a bodiless, typeless 200-shaped nothing:
	// that is what "silently shipped without a UI" looks like from a browser.
	if rec.Header().Get("Content-Type") == "" {
		t.Errorf("no Content-Type on a %d: a client cannot tell what it got", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Errorf("empty body on a %d", rec.Code)
	}
}
