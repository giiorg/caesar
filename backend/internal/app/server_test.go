package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/giiorg/caesar/backend/internal/webui"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func discard() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

// getHealthzWithPinger is shared with serve_integration_test.go (same package).
func getHealthzWithPinger(t *testing.T, db Pinger) (*http.Response, map[string]any) {
	t.Helper()
	srv := httptest.NewServer(Routes(discard(), db, nil, "http://localhost:8080", webui.Handler()))
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestHealthz_ReportsOKWhenDatabaseReachable(t *testing.T) {
	resp, body := getHealthzWithPinger(t, fakePinger{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body["status"] != "ok" || body["db"] != "ok" {
		t.Fatalf("body = %v", body)
	}
	if body["version"] == "" {
		t.Fatal("version missing from healthz body")
	}
}

func TestHealthz_Reports503WhenDatabaseDown(t *testing.T) {
	resp, body := getHealthzWithPinger(t, fakePinger{err: errors.New("conn refused")})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if body["status"] != "degraded" || body["db"] != "unreachable" {
		t.Fatalf("body = %v", body)
	}
}

func TestHealthz_SetsCorrelationIDHeader(t *testing.T) {
	resp, _ := getHealthzWithPinger(t, fakePinger{})
	if resp.Header.Get("X-Correlation-ID") == "" {
		t.Fatal("correlation id header missing")
	}
}

// recogniser is a stand-in UI handler that is trivially distinguishable from
// everything else Routes mounts. A real webui.Handler() would work too, but its
// answer depends on which build tag compiled, so a test using it would assert a
// different thing in each lane.
func recogniser() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>ui</html>"))
	})
}

func TestUnknownAPIPathStaysProblemJSONRatherThanBecomingTheSPAFallback(t *testing.T) {
	// The regression this guards is quiet and total: a root handler registered
	// without the /api/ catch-all would answer every mistyped endpoint with
	// index.html, and a client would parse HTML as JSON forever.
	h := Routes(discard(), fakePinger{}, nil, "http://caesar.test", recogniser())
	// No ".." case here on purpose: http.ServeMux cleans the path and answers
	// 307 before any handler runs (MEASURED), so such a case would be asserting
	// stdlib routing rather than this function's pattern set.
	for _, path := range []string{"/api/v1/nope", "/api/", "/api/v1/auth/not-a-real-endpoint"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 (body: %.80q)", path, rec.Code, rec.Body.String())
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("GET %s Content-Type = %q, want application/problem+json", path, ct)
		}
		// The positive half: it must be the typed envelope, not merely a 404
		// that happens to be JSON-shaped.
		var pd struct {
			Type   string `json:"type"`
			Status int    `json:"status"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&pd); err != nil {
			t.Errorf("GET %s body is not JSON: %v", path, err)
			continue
		}
		if pd.Status != http.StatusNotFound || pd.Type == "" {
			t.Errorf("GET %s problem envelope = %+v", path, pd)
		}
	}
}

// TestUIIsMountedAtTheRoot is the other half of the test above: a Routes that
// simply refused everything under /api/ would satisfy it while never mounting
// the UI at all, and nothing in this package would notice.
func TestUIIsMountedAtTheRoot(t *testing.T) {
	h := Routes(discard(), fakePinger{}, nil, "http://caesar.test", recogniser())
	for _, path := range []string{"/", "/approvals", "/deep/link"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Body.String() != "<html>ui</html>" {
			t.Errorf("GET %s did not reach the UI handler: %d %.80q", path, rec.Code, rec.Body.String())
		}
	}
}

// TestHealthzSurvivesTheRootMount pins the ServeMux precedence the comment in
// Routes claims. "GET /healthz" is more specific than "/", so it wins — but
// that is a property of the pattern set, and the whole point of registering the
// UI last is that it must not swallow the health endpoint.
//
// The non-GET half is the one that actually caught something. MEASURED: with a
// bare `mux.Handle("/", ui)`, POST/PUT/DELETE /healthz answered 200
// "<html>ui</html>" instead of the 405 the pattern set produced before the UI
// was mounted — and answered 200 even with the database down, so a probe using
// any method but GET would have reported a dead process healthy.
func TestHealthzSurvivesTheRootMount(t *testing.T) {
	h := Routes(discard(), fakePinger{}, nil, "http://caesar.test", recogniser())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("/healthz Content-Type = %q, want application/json — the UI mount swallowed it", ct)
	}

	// A dead database, so a wrong answer here is unmistakable: the UI handler
	// does not consult the Pinger and would answer 200 regardless.
	down := Routes(discard(), fakePinger{err: errors.New("db down")}, nil, "http://caesar.test", recogniser())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		down.ServeHTTP(rec, httptest.NewRequest(method, "/healthz", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /healthz = %d, want 405 — the UI mount swallowed the method mismatch (body: %.40q)",
				method, rec.Code, rec.Body.String())
		}
		if allow := rec.Header().Get("Allow"); allow == "" {
			t.Errorf("%s /healthz sent no Allow header; a 405 without one tells a client nothing", method)
		}
	}
}

// TestUIRefusesMethodsItCannotServe: the root mount is a static file server.
// Registering it as a bare "/" hands it every method on every unmatched path,
// which is how the /healthz 405 above was lost. Registering `GET /` instead is
// NOT the fix — MEASURED, it panics at registration:
//
//	pattern "GET /" … conflicts with pattern "/api/": GET / matches fewer
//	methods than /api/, but has a more general path pattern
func TestUIRefusesMethodsItCannotServe(t *testing.T) {
	h := Routes(discard(), fakePinger{}, nil, "http://caesar.test", recogniser())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		// No Origin header: CheckOrigin only rejects a PRESENT mismatched one,
		// so this reaches the mux and isolates the routing decision.
		h.ServeHTTP(rec, httptest.NewRequest(method, "/approvals", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /approvals = %d, want 405 (body: %.40q)", method, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("%s /approvals Allow = %q, want %q", method, got, "GET, HEAD")
		}
	}
	// HEAD must still reach the UI: browsers and probes use it, and a static
	// file server answers it natively.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/approvals", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD /approvals = %d, want 200", rec.Code)
	}
}

// directive pulls one CSP directive out verbatim. A four-line local copy of
// httpx's helper: that one lives in package httpx_test and is not importable.
func directive(csp, name string) string {
	for _, d := range strings.Split(csp, ";") {
		if d = strings.TrimSpace(d); d == name || strings.HasPrefix(d, name+" ") {
			return d
		}
	}
	return ""
}

// TestRoutesSetsTheSecurityHeaders pins that SecurityHeaders is in the chain
// AND that it is the production policy. httpx's own tests prove the middleware
// produces the right headers for each mode; only this one proves caesar's HTTP
// surface wears the right mode.
//
// Asserting mere presence is not enough, and that is MEASURED rather than
// argued: changing httpx.SecurityHeaders(false) to (true) in server.go left the
// whole ./internal/app/ suite green while the served policy became
// `script-src 'self' 'unsafe-inline' 'unsafe-eval'` with no HSTS at all.
func TestRoutesSetsTheSecurityHeaders(t *testing.T) {
	h := Routes(discard(), fakePinger{}, nil, "http://caesar.test", recogniser())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy on the served UI")
	}
	// Whole-directive equality. strings.Contains(csp, "script-src 'self'") is
	// satisfied by the dev policy, which is exactly the regression that matters.
	if got := directive(csp, "script-src"); got != "script-src 'self'" {
		t.Errorf("script-src = %q, want %q — Routes must serve the PRODUCTION policy", got, "script-src 'self'")
	}
	if got := directive(csp, "connect-src"); got != "connect-src 'self'" {
		t.Errorf("connect-src = %q, want %q", got, "connect-src 'self'")
	}
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=31536000; includeSubDomains" {
		t.Errorf("Strict-Transport-Security = %q, want the production value — dev sends none", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}
