//go:build integration

package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/app"
	"github.com/giiorg/caesar/backend/internal/assets"
	assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"
	"github.com/giiorg/caesar/backend/internal/identity"
	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs"
	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
	"github.com/giiorg/caesar/backend/internal/webui"
)

// The month-one auth loop, through the real Routes wiring: bootstrap →
// login → me → unknown API path is problem+json.
func TestAppRoutes_AuthLoopEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	svc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	if _, err := svc.Bootstrap(ctx, "root@example.com", "a long enough password"); err != nil {
		t.Fatal(err)
	}

	keys, err := secrets.ParseKeyset("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	assetsSvc := assets.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), keys)

	ops := identityapi.Operations(identityapi.New(svc, 90*24*time.Hour, assetsSvc))
	// identity.Service serves as authenticator, authorizer, and grant
	// source for the assets API, exactly as serve.go wires it.
	ops = append(ops, assetsapi.Operations(assetsapi.New(assetsSvc, svc, svc, svc))...)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(app.Routes(log, pool, ops, "http://caesar.test", webui.Handler()))
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]string{"email": "root@example.com", "password": "a long enough password"})
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	var token string
	for _, c := range resp.Cookies() {
		if c.Name == identityapi.SessionCookie {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no session cookie")
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/auth/me", nil)
	req.Header.Set("Cookie", identityapi.SessionCookie+"="+token)
	me, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me = %d", me.StatusCode)
	}

	// The bootstrapped admin can create a project through the real
	// identity↔assets wiring (identity.Service as authenticator,
	// authorizer, and grant source all at once).
	createBody, _ := json.Marshal(assetsapi.CreateProjectRequest{Name: "alpha"})
	createReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/projects", bytes.NewReader(createBody))
	createReq.Header.Set("Cookie", identityapi.SessionCookie+"="+token)
	createReq.Header.Set("Content-Type", "application/json")
	created, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatal(err)
	}
	defer created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create project = %d", created.StatusCode)
	}
	var project assetsapi.ProjectResponse
	if err := json.NewDecoder(created.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	if project.Name != "alpha" {
		t.Fatalf("created project name = %q, want alpha", project.Name)
	}

	listReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/projects", nil)
	listReq.Header.Set("Cookie", identityapi.SessionCookie+"="+token)
	list, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list projects = %d", list.StatusCode)
	}
	var listed assetsapi.ProjectListResponse
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range listed.Projects {
		if p.ID == project.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("created project %s not found in list %+v", project.ID, listed.Projects)
	}

	// Unknown API path → problem+json 404, not the stdlib text page.
	nf, err := http.Get(srv.URL + "/api/v1/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer nf.Body.Close()
	if nf.StatusCode != http.StatusNotFound || nf.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unknown path: %d %s", nf.StatusCode, nf.Header.Get("Content-Type"))
	}
}

// The gate surface mounted on the REAL Routes, behind the REAL identity
// middleware: a session the identity context issued reaches both new handlers,
// and no session reaches neither.
//
// This is a routing and authentication proof and nothing more. What a decision
// does once it lands — the CAS, the authorization, the revision — is settled in
// internal/runs; that a human can drive the whole conveyor through these routes
// is the golden set's job (Task 13).
//
// The spine's ports are nil on purpose. A route-reachability proof must not
// reach them, and nil is the only double that says so audibly: a handler that
// started needing a definition, a repo or a forge would panic here rather than
// pass against a stub that quietly answered.
func TestAppRoutes_GateSurfaceIsReachableThroughTheRealMiddleware(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const password = "a long enough password"
	identitySvc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	if _, err := identitySvc.Bootstrap(ctx, "root@example.com", password); err != nil {
		t.Fatal(err)
	}
	keys, err := secrets.ParseKeyset("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	assetsSvc := assets.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), keys)
	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		nil, nil, nil, nil, nil, nil, runs.Params{Snapshot: runs.DefaultSnapshot()})

	ops := identityapi.Operations(identityapi.New(identitySvc, 90*24*time.Hour, assetsSvc))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ops = append(ops, runsapi.Operations(runsapi.New(runsSvc, identitySvc, identitySvc, identitySvc, nil, log))...)
	srv := httptest.NewServer(app.Routes(log, pool, ops, "http://caesar.test", webui.Handler()))
	t.Cleanup(srv.Close)

	loginBody, _ := json.Marshal(map[string]string{"email": "root@example.com", "password": password})
	loginResp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", loginResp.StatusCode)
	}
	var token string
	for _, c := range loginResp.Cookies() {
		if c.Name == identityapi.SessionCookie {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no session cookie")
	}

	call := func(t *testing.T, method, path, cookie string, body any) *http.Response {
		t.Helper()
		var buf bytes.Buffer
		if body != nil {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, srv.URL+path, &buf)
		if err != nil {
			t.Fatal(err)
		}
		if cookie != "" {
			req.Header.Set("Cookie", identityapi.SessionCookie+"="+cookie)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	decision := "/api/v1/runs/" + uuid.NewString() + "/gates/gate/decision"
	approve := runsapi.GateDecisionRequest{Decision: "approve"}

	t.Run("the inbox answers a real session", func(t *testing.T) {
		resp := call(t, http.MethodGet, "/api/v1/approvals", token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("approvals = %d, want 200", resp.StatusCode)
		}
		var list runsapi.ApprovalListResponse
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatal(err)
		}
		if len(list.Approvals) != 0 {
			t.Errorf("approvals = %+v, want none: this database has no runs", list.Approvals)
		}
	})

	t.Run("the decision route reaches the handler", func(t *testing.T) {
		// 404 rather than 405 or the stdlib page: the wildcard pattern matched,
		// the handler ran, and the spine answered about the run.
		resp := call(t, http.MethodPost, decision, token, approve)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("decision on an unknown run = %d, want 404", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("content-type = %q, want problem+json", ct)
		}
	})

	t.Run("neither route answers without a session", func(t *testing.T) {
		for _, tc := range []struct {
			method, path string
			body         any
		}{
			{http.MethodGet, "/api/v1/approvals", nil},
			{http.MethodPost, decision, approve},
		} {
			if resp := call(t, tc.method, tc.path, "", tc.body); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s anonymous = %d, want 401", tc.method, tc.path, resp.StatusCode)
			}
		}
	})
}
