//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/workflows"
	workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"
)

type fakeAuth struct {
	sessions map[string]authn.AuthSession
}

func (f *fakeAuth) AuthenticateSession(_ context.Context, token string) (authn.AuthSession, error) {
	s, ok := f.sessions[token]
	if !ok {
		return authn.AuthSession{}, xerrors.E(xerrors.ClassUnauthenticated, "invalid or expired session")
	}
	return s, nil
}

// newTestServer seeds one admin ("admin-token") and one member
// ("member-token") and mounts the real route table over the real
// service against a real database.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	pool := testdb.New(t)
	fake := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	svc := workflows.NewService(pool, fake, audit.NewRecorder(fake))

	auth := &fakeAuth{sessions: map[string]authn.AuthSession{}}
	for token, role := range map[string]authn.Role{"admin-token": authn.RoleAdmin, "member-token": authn.RoleMember} {
		var id uuid.UUID
		err := pool.QueryRow(context.Background(),
			"INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', $2) RETURNING id",
			token+"@example.com", string(role)).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		auth.sessions[token] = authn.AuthSession{Principal: authn.Principal{UserID: id, Email: token + "@example.com", Role: role}, SessionID: uuid.New()}
	}

	mux := http.NewServeMux()
	httpx.Mount(mux, workflowsapi.Operations(workflowsapi.New(svc, auth)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path, token string, body any) *http.Response {
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
	if token != "" {
		req.Header.Set("Cookie", authn.SessionCookie+"="+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

const testDoc = `{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "harness": "cc", "instructions": "x"}]}`

func TestWorkflowsAPI_AdminWritesMemberReads(t *testing.T) {
	srv := newTestServer(t)

	created := do(t, srv, http.MethodPost, "/api/v1/workflows", "admin-token",
		workflowsapi.CreateWorkflowRequest{Name: "conveyor"})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create workflow = %d", created.StatusCode)
	}
	var wf workflowsapi.WorkflowResponse
	if err := json.NewDecoder(created.Body).Decode(&wf); err != nil {
		t.Fatal(err)
	}

	post := do(t, srv, http.MethodPost, "/api/v1/workflows/"+wf.ID+"/versions", "admin-token",
		workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(testDoc)})
	if post.StatusCode != http.StatusCreated {
		t.Fatalf("create version = %d", post.StatusCode)
	}

	// Idempotent replay: same content → 200, same version.
	replay := do(t, srv, http.MethodPost, "/api/v1/workflows/"+wf.ID+"/versions", "admin-token",
		workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(testDoc)})
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("idempotent replay = %d, want 200", replay.StatusCode)
	}

	got := do(t, srv, http.MethodGet, "/api/v1/workflows/"+wf.ID+"/versions/1", "member-token", nil)
	if got.StatusCode != http.StatusOK {
		t.Fatalf("member get version = %d", got.StatusCode)
	}
	var v workflowsapi.WorkflowVersionResponse
	if err := json.NewDecoder(got.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || len(v.Document) == 0 || v.ContentHash == "" {
		t.Fatalf("version response: %+v", v)
	}

	list := do(t, srv, http.MethodGet, "/api/v1/workflows", "member-token", nil)
	if list.StatusCode != http.StatusOK {
		t.Fatalf("member list = %d", list.StatusCode)
	}
}

func TestWorkflowsAPI_RBACAndAuth(t *testing.T) {
	srv := newTestServer(t)

	if resp := do(t, srv, http.MethodPost, "/api/v1/workflows", "member-token",
		workflowsapi.CreateWorkflowRequest{Name: "nope"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member create = %d, want 403", resp.StatusCode)
	}
	if resp := do(t, srv, http.MethodGet, "/api/v1/workflows", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous list = %d, want 401", resp.StatusCode)
	}
	if resp := do(t, srv, http.MethodGet, "/api/v1/workflows", "bogus", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bogus token = %d, want 401", resp.StatusCode)
	}
}

func TestWorkflowsAPI_ValidationSurfacesFieldErrors(t *testing.T) {
	srv := newTestServer(t)
	created := do(t, srv, http.MethodPost, "/api/v1/workflows", "admin-token",
		workflowsapi.CreateWorkflowRequest{Name: "conveyor"})
	var wf workflowsapi.WorkflowResponse
	if err := json.NewDecoder(created.Body).Decode(&wf); err != nil {
		t.Fatal(err)
	}

	bad := `{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "instructions": "x"}]}`
	resp := do(t, srv, http.MethodPost, "/api/v1/workflows/"+wf.ID+"/versions", "admin-token",
		workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(bad)})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid doc = %d, want 400", resp.StatusCode)
	}
	var pd httpx.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&pd); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fe := range pd.Errors {
		if fe.Field == "nodes[0].harness" {
			found = true
		}
	}
	if !found {
		t.Fatalf("problem errors missing nodes[0].harness: %+v", pd.Errors)
	}

	if resp := do(t, srv, http.MethodGet, "/api/v1/workflows/"+wf.ID+"/versions/notanumber", "member-token", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("bad version segment = %d, want 404", resp.StatusCode)
	}
}
