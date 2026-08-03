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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/app"
	"github.com/giiorg/caesar/backend/internal/identity"
	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/workflows"
	workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"
)

// The definitions loop through the real Routes wiring: bootstrap →
// login → create workflow → post a version through the write door →
// read the stored document back.
func TestAppRoutes_WorkflowsEndToEnd(t *testing.T) {
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
	workflowsSvc := workflows.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}))

	ops := identityapi.Operations(identityapi.New(svc, 90*24*time.Hour, nil))
	ops = append(ops, workflowsapi.Operations(workflowsapi.New(workflowsSvc, svc))...)
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(app.Routes(log, pool, ops, "http://caesar.test"))
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

	post := func(path string, payload any) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
		req.Header.Set("Cookie", identityapi.SessionCookie+"="+token)
		req.Header.Set("Content-Type", "application/json")
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Body.Close() })
		return r
	}

	created := post("/api/v1/workflows", workflowsapi.CreateWorkflowRequest{Name: "conveyor"})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create workflow = %d", created.StatusCode)
	}
	var wf workflowsapi.WorkflowResponse
	if err := json.NewDecoder(created.Body).Decode(&wf); err != nil {
		t.Fatal(err)
	}

	doc := `{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "harness": "cc", "instructions": "implement it"}]}`
	posted := post("/api/v1/workflows/"+wf.ID+"/versions", workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(doc)})
	if posted.StatusCode != http.StatusCreated {
		t.Fatalf("create version = %d", posted.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/workflows/"+wf.ID+"/versions/1", nil)
	req.Header.Set("Cookie", identityapi.SessionCookie+"="+token)
	got, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusOK {
		t.Fatalf("get version = %d", got.StatusCode)
	}
	var v workflowsapi.WorkflowVersionResponse
	if err := json.NewDecoder(got.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || v.ContentHash == "" || len(v.Document) == 0 {
		t.Fatalf("version = %+v", v)
	}
}
