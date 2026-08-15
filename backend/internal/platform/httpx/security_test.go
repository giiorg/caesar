package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"
)

// directive pulls one CSP directive out verbatim so a test can compare the
// WHOLE thing. strings.Contains(csp, "script-src 'self'") is satisfied by
// "script-src 'self' https://cdn.example" and by "script-src 'self'
// 'unsafe-eval'" — i.e. it passes while the property it exists for is broken.
func directive(csp, name string) (string, bool) {
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if d == name || strings.HasPrefix(d, name+" ") {
			return d, true
		}
	}
	return "", false
}

func TestSecurityHeaders(t *testing.T) {
	h := httpx.Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), httpx.SecurityHeaders(false))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	// Whole-directive equality, not substring containment: every one of these
	// is the complete text the browser must see.
	for name, want := range map[string]string{
		"default-src":     "default-src 'self'",
		"script-src":      "script-src 'self'",
		"style-src":       "style-src 'self' 'unsafe-inline'",
		"img-src":         "img-src 'self' data:",
		"font-src":        "font-src 'self'",
		"connect-src":     "connect-src 'self'",
		"form-action":     "form-action 'self'",
		"frame-ancestors": "frame-ancestors 'none'",
		"base-uri":        "base-uri 'none'",
		"object-src":      "object-src 'none'",
	} {
		got, ok := directive(csp, name)
		if !ok {
			t.Errorf("CSP has no %s directive: %s", name, csp)
			continue
		}
		if got != want {
			t.Errorf("CSP %s = %q, want %q", name, got, want)
		}
	}
	// script-src is the property that matters and it admits no unsafe source
	// and no foreign origin: the self-hosted fonts of the theme mean no
	// third-party origin needs allowing, which is what keeps this policy short.
	// The equality check above already forbids both; this is the message a
	// future reader gets if someone widens it.
	if got, _ := directive(csp, "script-src"); strings.Contains(got, "unsafe") || strings.Contains(got, "//") {
		t.Errorf("script-src must admit no unsafe source and no foreign origin: %q", got)
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"Referrer-Policy":            "same-origin",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Strict-Transport-Security":  "max-age=31536000; includeSubDomains",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestSecurityHeadersDevRelaxesOnlyWhatHMRNeeds(t *testing.T) {
	h := httpx.Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), httpx.SecurityHeaders(true))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "ws:") {
		t.Errorf("dev CSP must allow the HMR websocket: %s", csp)
	}
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("dev must not send HSTS: a stuck HSTS entry on localhost breaks every other local http service")
	}
	// The relaxation is targeted. A dev build that also dropped
	// frame-ancestors or object-src would be a different policy being
	// exercised in the dev loop from the one that ships — the "only what HMR
	// needs" in this test's name is the assertion, not the comment.
	for _, want := range []string{"frame-ancestors 'none'", "base-uri 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("dev CSP dropped %q, which HMR does not need relaxed: %s", want, csp)
		}
	}
	// connect-src is what the HMR websocket actually needs; script-src is
	// relaxed for the injected module graph. Nothing else moves.
	if got, _ := directive(csp, "connect-src"); !strings.Contains(got, "ws:") {
		t.Errorf("dev connect-src = %q, want the ws:/wss: sources", got)
	}
}
