//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// fakePorts implements the authn ports and GrantSource without identity:
// runs tests may never import the identity context (arch law). Real
// wiring is exercised by the app e2e (Task 12).
type fakePorts struct {
	sessions map[string]authn.AuthSession
	grants   map[uuid.UUID][]authn.Grant
}

func (f *fakePorts) AuthenticateSession(_ context.Context, token string) (authn.AuthSession, error) {
	s, ok := f.sessions[token]
	if !ok {
		return authn.AuthSession{}, xerrors.E(xerrors.ClassUnauthenticated, "invalid or expired session")
	}
	return s, nil
}

func (f *fakePorts) Authorize(_ context.Context, p authn.Principal, projectID uuid.UUID, min authn.GrantLevel) (bool, error) {
	if p.Role == authn.RoleAdmin {
		return true, nil
	}
	for _, g := range f.grants[p.UserID] {
		if g.ProjectID == projectID && g.Level.AtLeast(min) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakePorts) GrantsForUser(_ context.Context, userID uuid.UUID) ([]authn.Grant, error) {
	return f.grants[userID], nil
}

// spinePorts serves the harness's own rows through the runs ports — the
// spine reaches peer-context assets only this way.
type spinePorts struct {
	h *harness
}

func (p spinePorts) Definition(_ context.Context, workflowID uuid.UUID, version int32) (runs.PinnedDefinition, error) {
	if workflowID != p.h.workflowID || version != 1 {
		return runs.PinnedDefinition{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	return p.pinned(), nil
}

func (p spinePorts) DefinitionByVersionID(_ context.Context, versionID uuid.UUID) (runs.PinnedDefinition, error) {
	if versionID != p.h.versionID {
		return runs.PinnedDefinition{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	return p.pinned(), nil
}

func (p spinePorts) pinned() runs.PinnedDefinition {
	return runs.PinnedDefinition{
		VersionID: p.h.versionID, WorkflowID: p.h.workflowID, Version: 1,
		ContentHash: "deadbeef", Doc: agenticDoc(),
	}
}

func (p spinePorts) Repo(_ context.Context, id uuid.UUID) (runs.RepoRef, error) {
	if id != p.h.repoID {
		return runs.RepoRef{}, xerrors.E(xerrors.ClassNotFound, "no such repo")
	}
	return runs.RepoRef{
		ID: p.h.repoID, ProjectID: p.h.projectID, Forge: "github",
		Slug: "spine/repo", DefaultBranch: "main",
	}, nil
}

func (p spinePorts) HarnessByName(_ context.Context, name string) (runs.HarnessRef, error) {
	if name != "cc" {
		return runs.HarnessRef{}, xerrors.E(xerrors.ClassNotFound, "no such harness")
	}
	return runs.HarnessRef{ID: uuid.Max, Kind: "claude-code"}, nil
}

func agenticDoc() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement the plan."},
		},
		Edges: []runs.DefEdge{{From: "plan", To: "impl", Artifact: runs.ArtifactKindPlan}},
	}
}

// harness mounts the real route table over the real service against a
// real database: one admin, one member per grant level, and the asset
// rows the spine's foreign keys point at.
type harness struct {
	srv        *httptest.Server
	pool       *pgxpool.Pool
	ports      *fakePorts
	projectID  uuid.UUID
	repoID     uuid.UUID
	workflowID uuid.UUID
	versionID  uuid.UUID
	admin      authn.Principal
	runner     authn.Principal
	viewer     authn.Principal
	outsider   authn.Principal
}

func seedUser(t *testing.T, pool *pgxpool.Pool, email string, role authn.Role) authn.Principal {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		"INSERT INTO users (email, password_hash, role) VALUES ($1, 'x', $2) RETURNING id",
		email, string(role)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return authn.Principal{UserID: id, Email: email, Role: role}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	h := &harness{
		pool:  pool,
		ports: &fakePorts{sessions: map[string]authn.AuthSession{}, grants: map[uuid.UUID][]authn.Grant{}},
	}

	// Peer-context rows the spine's FKs require, inserted with raw SQL:
	// the runs context never imports assets or workflows.
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name) VALUES ('spine') RETURNING id`).Scan(&h.projectID); err != nil {
		t.Fatal(err)
	}
	var credID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext)
		 VALUES (uuidv7(), 'spine-token', 'forge_token', 'dev', '\x00', '\x00') RETURNING id`,
	).Scan(&credID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
		 VALUES ($1, 'github', 'spine/repo', 'main', $2) RETURNING id`,
		h.projectID, credID,
	).Scan(&h.repoID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflows (name) VALUES ('spine-wf') RETURNING id`).Scan(&h.workflowID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
		 VALUES ($1, 1, 'deadbeef', '{"schema":1,"nodes":[]}'::jsonb) RETURNING id`,
		h.workflowID,
	).Scan(&h.versionID); err != nil {
		t.Fatal(err)
	}

	h.admin = seedUser(t, pool, "root@example.com", authn.RoleAdmin)
	h.runner = seedUser(t, pool, "runner@example.com", authn.RoleMember)
	h.viewer = seedUser(t, pool, "viewer@example.com", authn.RoleMember)
	h.outsider = seedUser(t, pool, "outsider@example.com", authn.RoleMember)
	for token, p := range map[string]authn.Principal{
		"admin-tok": h.admin, "runner-tok": h.runner, "viewer-tok": h.viewer, "outsider-tok": h.outsider,
	} {
		h.ports.sessions[token] = authn.AuthSession{Principal: p, SessionID: uuid.New()}
	}
	h.ports.grants[h.runner.UserID] = []authn.Grant{{ProjectID: h.projectID, Level: authn.GrantRun}}
	h.ports.grants[h.viewer.UserID] = []authn.Grant{{ProjectID: h.projectID, Level: authn.GrantView}}

	c := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	ports := spinePorts{h: h}
	forge := fake.Forge{Heads: map[uuid.UUID]string{h.repoID: "basesha0000000000000000000000000000000000"}}
	svc := runs.NewService(pool, c, audit.NewRecorder(c), ports, ports, ports, forge,
		runs.Params{Snapshot: runs.DefaultSnapshot()})

	mux := http.NewServeMux()
	httpx.Mount(mux, runsapi.Operations(runsapi.New(svc, h.ports, h.ports, h.ports)))
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	h.srv = httptest.NewServer(httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log), httpx.CheckOrigin("http://caesar.test")))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) do(t *testing.T, method, path, token string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, h.srv.URL+path, &buf)
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

func decodeInto[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// newTask is the valid intake body every test starts from.
func (h *harness) newTask(title string) runsapi.CreateTaskRequest {
	return runsapi.CreateTaskRequest{
		RepoID: h.repoID.String(), WorkflowID: h.workflowID.String(), WorkflowVersion: 1,
		Title: title, Inputs: map[string]any{"hint": "small"},
	}
}

// createTask posts one task as the caller and fails unless it is created.
func (h *harness) createTask(t *testing.T, token, title string) runsapi.TaskResponse {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/api/v1/tasks", token, h.newTask(title))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task = %d, want 201", resp.StatusCode)
	}
	return decodeInto[runsapi.TaskResponse](t, resp)
}

func TestRunsAPI_CreateTaskRequiresRunGrant(t *testing.T) {
	h := newHarness(t)

	t.Run("run grant creates a pending task and its first run", func(t *testing.T) {
		task := h.createTask(t, "runner-tok", "ship it")
		if task.Status != string(runs.RunPending) {
			t.Errorf("status = %q, want pending", task.Status)
		}
		if task.LatestRunID == "" || task.LatestRunID == uuid.Nil.String() {
			t.Errorf("latest_run_id = %q, want a run id", task.LatestRunID)
		}
		if task.ProjectID != h.projectID.String() || task.RepoID != h.repoID.String() {
			t.Errorf("task projection wrong: %+v", task)
		}
		if task.RequesterID != h.runner.UserID.String() {
			t.Errorf("requester_id = %q, want the caller", task.RequesterID)
		}
	})

	t.Run("view grant is refused", func(t *testing.T) {
		resp := h.do(t, http.MethodPost, "/api/v1/tasks", "viewer-tok", h.newTask("nope"))
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("view-only create = %d, want 403", resp.StatusCode)
		}
		pd := decodeInto[httpx.ProblemDetails](t, resp)
		if pd.Detail != "insufficient project grant" {
			t.Errorf("detail = %q, want the grant message", pd.Detail)
		}
	})

	t.Run("anonymous is refused", func(t *testing.T) {
		if resp := h.do(t, http.MethodPost, "/api/v1/tasks", "", h.newTask("nope")); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous create = %d, want 401", resp.StatusCode)
		}
	})
}

func TestRunsAPI_CreateTaskValidatesFields(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name       string
		mutate     func(*runsapi.CreateTaskRequest)
		wantStatus int
		wantField  string
	}{
		{
			name:       "empty title",
			mutate:     func(req *runsapi.CreateTaskRequest) { req.Title = "" },
			wantStatus: http.StatusBadRequest, wantField: "title",
		},
		{
			name:       "workflow version below one",
			mutate:     func(req *runsapi.CreateTaskRequest) { req.WorkflowVersion = 0 },
			wantStatus: http.StatusBadRequest, wantField: "workflow_version",
		},
		{
			name:       "unknown repo",
			mutate:     func(req *runsapi.CreateTaskRequest) { req.RepoID = uuid.New().String() },
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "malformed repo id",
			mutate:     func(req *runsapi.CreateTaskRequest) { req.RepoID = "not-a-uuid" },
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "unknown workflow",
			mutate:     func(req *runsapi.CreateTaskRequest) { req.WorkflowID = uuid.New().String() },
			wantStatus: http.StatusNotFound,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := h.newTask("ship it")
			tc.mutate(&req)
			resp := h.do(t, http.MethodPost, "/api/v1/tasks", "runner-tok", req)
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			pd := decodeInto[httpx.ProblemDetails](t, resp)
			if tc.wantField == "" {
				return
			}
			blamed := false
			for _, fe := range pd.Errors {
				if fe.Field == tc.wantField {
					blamed = true
				}
			}
			if !blamed {
				t.Errorf("errors = %+v, want one blaming %q", pd.Errors, tc.wantField)
			}
		})
	}
}

func TestRunsAPI_ListTasksFollowsGrants(t *testing.T) {
	h := newHarness(t)
	task := h.createTask(t, "admin-tok", "ship it")

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"admin sees every task", "admin-tok", 1},
		{"granted member sees the task", "viewer-tok", 1},
		{"ungranted member sees nothing", "outsider-tok", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := h.do(t, http.MethodGet, "/api/v1/tasks", tc.token, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("list = %d, want 200", resp.StatusCode)
			}
			got := decodeInto[runsapi.TaskListResponse](t, resp)
			if len(got.Tasks) != tc.want {
				t.Fatalf("list = %d tasks, want %d", len(got.Tasks), tc.want)
			}
			if tc.want == 1 && got.Tasks[0].ID != task.ID {
				t.Errorf("listed %q, want %q", got.Tasks[0].ID, task.ID)
			}
		})
	}
}

func TestRunsAPI_ReadEndpointsRequireViewGrant(t *testing.T) {
	h := newHarness(t)
	task := h.createTask(t, "admin-tok", "ship it")
	unknown := uuid.New().String()

	cases := []struct {
		name string
		path string
	}{
		{"task", "/api/v1/tasks/" + task.ID},
		{"task runs", "/api/v1/tasks/" + task.ID + "/runs"},
		{"run", "/api/v1/runs/" + task.LatestRunID},
		{"run events", "/api/v1/runs/" + task.LatestRunID + "/events"},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is readable with view", func(t *testing.T) {
			if resp := h.do(t, http.MethodGet, tc.path, "viewer-tok", nil); resp.StatusCode != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, resp.StatusCode)
			}
		})
		t.Run(tc.name+" is refused without a grant", func(t *testing.T) {
			resp := h.do(t, http.MethodGet, tc.path, "outsider-tok", nil)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("GET %s = %d, want 403", tc.path, resp.StatusCode)
			}
			if pd := decodeInto[httpx.ProblemDetails](t, resp); pd.Detail != "insufficient project grant" {
				t.Errorf("detail = %q, want the grant message", pd.Detail)
			}
		})
	}

	t.Run("unknown ids are not found", func(t *testing.T) {
		for _, path := range []string{
			"/api/v1/tasks/" + unknown,
			"/api/v1/tasks/" + unknown + "/runs",
			"/api/v1/runs/" + unknown,
			"/api/v1/runs/" + unknown + "/events",
			"/api/v1/tasks/not-a-uuid",
		} {
			if resp := h.do(t, http.MethodGet, path, "admin-tok", nil); resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
			}
		}
	})

	t.Run("a run carries its phases and artifact metadata inline", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, "/api/v1/runs/"+task.LatestRunID, "viewer-tok", nil)
		run := decodeInto[runsapi.RunResponse](t, resp)
		if run.ID != task.LatestRunID || run.TaskID != task.ID {
			t.Errorf("run identity wrong: %+v", run)
		}
		if run.Status != string(runs.RunPending) || run.DefinitionHash != "deadbeef" {
			t.Errorf("run pins wrong: %+v", run)
		}
		if run.PolicySnapshot != runs.DefaultSnapshot() {
			t.Errorf("policy_snapshot = %+v, want the pinned default", run.PolicySnapshot)
		}
		// Intake mints no phases; the orchestrator materializes them.
		if len(run.Phases) != 0 || len(run.Artifacts) != 0 {
			t.Errorf("phases/artifacts = %d/%d, want none at intake", len(run.Phases), len(run.Artifacts))
		}
	})

	t.Run("a task's runs list carries no phases", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, "/api/v1/tasks/"+task.ID+"/runs", "viewer-tok", nil)
		list := decodeInto[runsapi.RunListResponse](t, resp)
		if len(list.Runs) != 1 || list.Runs[0].ID != task.LatestRunID {
			t.Fatalf("runs = %+v, want the one run", list.Runs)
		}
		if len(list.Runs[0].Phases) != 0 || len(list.Runs[0].Artifacts) != 0 {
			t.Errorf("list entries must stay shallow: %+v", list.Runs[0])
		}
	})
}

func TestRunsAPI_RerunSupersedesPendingAndRefusesLive(t *testing.T) {
	h := newHarness(t)
	task := h.createTask(t, "admin-tok", "ship it")

	t.Run("view grant cannot re-run", func(t *testing.T) {
		if resp := h.do(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/runs", "viewer-tok", nil); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("view-only rerun = %d, want 403", resp.StatusCode)
		}
	})

	var second runsapi.RunResponse
	t.Run("re-running a pending run supersedes it", func(t *testing.T) {
		resp := h.do(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/runs", "runner-tok", nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("rerun = %d, want 201", resp.StatusCode)
		}
		second = decodeInto[runsapi.RunResponse](t, resp)
		if second.ID == task.LatestRunID || second.Status != string(runs.RunPending) {
			t.Fatalf("second run = %+v, want a new pending run", second)
		}
		first := decodeInto[runsapi.RunResponse](t, h.do(t, http.MethodGet, "/api/v1/runs/"+task.LatestRunID, "runner-tok", nil))
		if first.Status != string(runs.RunSuperseded) {
			t.Errorf("first run = %q, want superseded", first.Status)
		}
		if first.Outcome == nil || first.Outcome.Code != runs.CodeSuperseded {
			t.Errorf("first run outcome = %+v, want the superseded code", first.Outcome)
		}
	})

	t.Run("re-running a live run is a conflict", func(t *testing.T) {
		if _, err := h.pool.Exec(context.Background(),
			`UPDATE runs SET status='running', started_at=now() WHERE id=$1`, second.ID); err != nil {
			t.Fatal(err)
		}
		if resp := h.do(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/runs", "runner-tok", nil); resp.StatusCode != http.StatusConflict {
			t.Fatalf("rerun of a running run = %d, want 409", resp.StatusCode)
		}
	})
}

func TestRunsAPI_ListRunEventsPagesByID(t *testing.T) {
	h := newHarness(t)
	task := h.createTask(t, "admin-tok", "ship it")
	// The re-run gives the first run a second event to page over.
	if resp := h.do(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/runs", "runner-tok", nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("rerun = %d, want 201", resp.StatusCode)
	}
	base := "/api/v1/runs/" + task.LatestRunID + "/events"

	var last int64
	t.Run("after zero returns every event in id order", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, base+"?after=0", "viewer-tok", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("events = %d, want 200", resp.StatusCode)
		}
		got := decodeInto[runsapi.RunEventsResponse](t, resp)
		if len(got.Events) != 2 {
			t.Fatalf("events = %+v, want two", got.Events)
		}
		if got.Events[0].Kind != "run.created" || got.Events[1].Kind != "run.superseded" {
			t.Errorf("kinds = %q, %q, want run.created then run.superseded", got.Events[0].Kind, got.Events[1].Kind)
		}
		if got.Events[0].ID >= got.Events[1].ID {
			t.Errorf("ids %d, %d are not ascending", got.Events[0].ID, got.Events[1].ID)
		}
		if got.Events[0].Payload["task_id"] != task.ID {
			t.Errorf("run.created payload = %+v, want the task id", got.Events[0].Payload)
		}
		last = got.Events[1].ID
	})

	t.Run("an absent cursor is the same as zero", func(t *testing.T) {
		got := decodeInto[runsapi.RunEventsResponse](t, h.do(t, http.MethodGet, base, "viewer-tok", nil))
		if len(got.Events) != 2 {
			t.Fatalf("events = %+v, want two", got.Events)
		}
	})

	t.Run("after the last id returns nothing", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, base+"?after="+strconv.FormatInt(last, 10), "viewer-tok", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("events = %d, want 200", resp.StatusCode)
		}
		if got := decodeInto[runsapi.RunEventsResponse](t, resp); len(got.Events) != 0 {
			t.Fatalf("events = %+v, want none", got.Events)
		}
	})

	t.Run("a junk cursor blames the after field", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, base+"?after=soon", "viewer-tok", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("junk cursor = %d, want 400", resp.StatusCode)
		}
		pd := decodeInto[httpx.ProblemDetails](t, resp)
		if len(pd.Errors) != 1 || pd.Errors[0].Field != "after" {
			t.Errorf("errors = %+v, want one blaming after", pd.Errors)
		}
	})
}
