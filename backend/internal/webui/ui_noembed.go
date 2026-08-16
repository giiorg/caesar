//go:build !embedui

// The package doc lives in ui.go, which carries no build tag: a doc comment on
// a tagged file is a doc comment the other build does not have.

package webui

import (
	"encoding/json"
	"net/http"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

// Handler is the tagless twin: it says out loud that no UI is embedded.
//
// This shape is chosen over a committed placeholder dist specifically for its
// failure mode. A placeholder that a forgotten `vite build` leaves in place
// ships a stub page silently; this says so — the same rule the architecture
// tests already enforce on the backend, where stubs return typed
// NotImplemented and never fake success.
//
// It also keeps //go:embed from failing to compile against an absent dist, so
// `task backend:test` stays Node-free and fast.
//
// The envelope is written here rather than through httpx.Problem, and that is
// a MEASURED correction, not a preference. httpx.Problem classifies via
// xerrors.ClassOf, and xerrors.NotImplemented deliberately carries no class —
// xerrors_test.go asserts "not-implemented sentinel defaults to internal" — so
// httpx.Problem(w, r, xerrors.NotImplemented(...)) answers **500** with the
// redacted detail "internal error". Both are wrong here: an operator staring
// at this response needs the status to say "unimplemented, not broken" and the
// detail to contain the word "embedui", which is the whole diagnostic value.
// Reclassifying NotImplemented inside xerrors would instead change every
// backend stub's status from 500 to 501, which is a taxonomy decision far
// outside a UI-serving handler.
//
// httpx.ProblemDetails is still the struct being marshalled, so the JSON field
// names remain single-sourced with every other error caesar emits.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(httpx.ProblemDetails{
			Type:   "/errors/not-implemented",
			Title:  "Not implemented",
			Status: http.StatusNotImplemented,
			Detail: "webui: this binary was built without the embedui tag, so no web client is embedded",
			// Correlation ids ride every response caesar writes (spec §4); a
			// refusal is no exception, or this one 501 is the single line an
			// operator cannot grep out of the access log.
			CorrelationID: httpx.CorrelationID(r.Context()),
		})
	})
}
