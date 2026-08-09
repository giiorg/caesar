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
	"strings"
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
		ContentHash: "deadbeef", Doc: gatedDoc(),
	}
}

func (p spinePorts) Repo(_ context.Context, id uuid.UUID) (runs.RepoRef, error) {
	projectID, ok := p.h.repos[id]
	if !ok {
		return runs.RepoRef{}, xerrors.E(xerrors.ClassNotFound, "no such repo")
	}
	return runs.RepoRef{
		ID: id, ProjectID: projectID, Forge: "github",
		Slug: "spine/repo", DefaultBranch: "main",
	}, nil
}

// Email is runs.PrincipalSource. The API suite never publishes, but loadPlan
// and the publish path take the port, so a double has to exist.
func (p spinePorts) Email(_ context.Context, userID uuid.UUID) (string, error) {
	return userID.String() + "@example.com", nil
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

// gatedDoc closes agenticDoc with the gate the decision endpoint decides and
// the publish that gate authorizes, so an approval here mints a real
// authorization rather than a nil one.
func gatedDoc() runs.DefinitionDoc {
	doc := agenticDoc()
	doc.Nodes = append(doc.Nodes,
		runs.DefNode{ID: "gate", Kind: runs.KindGate, Revision: &runs.DefRevision{Target: "impl", MaxRounds: 2}},
		runs.DefNode{ID: "publish", Kind: runs.KindGitPublish},
	)
	doc.Edges = append(doc.Edges,
		runs.DefEdge{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch},
		runs.DefEdge{From: "gate", To: "publish", Artifact: runs.ArtifactKindPatch},
	)
	return doc
}

// harness mounts the real route table over the real service against a
// real database: one admin, one member per grant level, and the asset
// rows the spine's foreign keys point at.
//
// TWO projects, each with its own repository and its own approver. The second
// one exists for the approvals inbox: with a gate on only one project, "the
// other approver sees nothing" is equally explained by "nothing is waiting
// there", and a requester-scoped inbox would pass every assertion.
type harness struct {
	srv        *httptest.Server
	pool       *pgxpool.Pool
	ports      *fakePorts
	projectID  uuid.UUID
	repoID     uuid.UUID
	projectB   uuid.UUID
	repoB      uuid.UUID
	repos      map[uuid.UUID]uuid.UUID // repo id -> project id
	workflowID uuid.UUID
	versionID  uuid.UUID
	admin      authn.Principal
	runner     authn.Principal
	approver   authn.Principal
	approverB  authn.Principal
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
	if err := pool.QueryRow(ctx, `INSERT INTO projects (name) VALUES ('spine-b') RETURNING id`).Scan(&h.projectB); err != nil {
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
		`INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
		 VALUES ($1, 'github', 'spine/repo-b', 'main', $2) RETURNING id`,
		h.projectB, credID,
	).Scan(&h.repoB); err != nil {
		t.Fatal(err)
	}
	h.repos = map[uuid.UUID]uuid.UUID{h.repoID: h.projectID, h.repoB: h.projectB}
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
	h.approver = seedUser(t, pool, "approver@example.com", authn.RoleMember)
	h.approverB = seedUser(t, pool, "approver-b@example.com", authn.RoleMember)
	h.viewer = seedUser(t, pool, "viewer@example.com", authn.RoleMember)
	h.outsider = seedUser(t, pool, "outsider@example.com", authn.RoleMember)
	for token, p := range map[string]authn.Principal{
		"admin-tok": h.admin, "runner-tok": h.runner, "approver-tok": h.approver,
		"approver-b-tok": h.approverB, "viewer-tok": h.viewer, "outsider-tok": h.outsider,
	} {
		h.ports.sessions[token] = authn.AuthSession{Principal: p, SessionID: uuid.New()}
	}
	h.ports.grants[h.runner.UserID] = []authn.Grant{{ProjectID: h.projectID, Level: authn.GrantRun}}
	h.ports.grants[h.approver.UserID] = []authn.Grant{{ProjectID: h.projectID, Level: authn.GrantApprove}}
	h.ports.grants[h.approverB.UserID] = []authn.Grant{{ProjectID: h.projectB, Level: authn.GrantApprove}}
	h.ports.grants[h.viewer.UserID] = []authn.Grant{{ProjectID: h.projectID, Level: authn.GrantView}}

	c := clock.NewFake(time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	ports := spinePorts{h: h}
	forge := fake.NewForge(map[uuid.UUID]string{
		h.repoID: "basesha0000000000000000000000000000000000",
		h.repoB:  "basesha0000000000000000000000000000000000",
	})
	svc := runs.NewService(pool, c, audit.NewRecorder(c), ports, ports, ports, forge,
		fake.NewPublisher(nil), ports, runs.Params{Snapshot: runs.DefaultSnapshot()})

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
	return h.newTaskInRepo(h.repoID, title)
}

func (h *harness) newTaskInRepo(repoID uuid.UUID, title string) runsapi.CreateTaskRequest {
	return runsapi.CreateTaskRequest{
		RepoID: repoID.String(), WorkflowID: h.workflowID.String(), WorkflowVersion: 1,
		Title: title, Inputs: map[string]any{"hint": "small"},
	}
}

// createTask posts one task as the caller and fails unless it is created.
func (h *harness) createTask(t *testing.T, token, title string) runsapi.TaskResponse {
	t.Helper()
	return h.createTaskInRepo(t, token, h.repoID, title)
}

func (h *harness) createTaskInRepo(t *testing.T, token string, repoID uuid.UUID, title string) runsapi.TaskResponse {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/api/v1/tasks", token, h.newTaskInRepo(repoID, title))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task = %d, want 201", resp.StatusCode)
	}
	return decodeInto[runsapi.TaskResponse](t, resp)
}

// stagedTreeHash is the git tree hash the staged patch claims. Any non-empty
// value satisfies mintAuthorization; nothing in this suite publishes.
const stagedTreeHash = "1111111111111111111111111111111111111111"

// stageGate parks a run at a gate awaiting a decision, without a conveyor.
//
// This suite answers HTTP and authorization questions — statuses, grants,
// scoping, body validation — and none of them need an agent to have run. What
// a materialized gate leaves behind is three writes, so they are made directly:
// the run waits, the examined patch exists with the tree hash an approval binds,
// and the gate's attempt row is waiting_decision. The conveyor that produces
// them for real is Task 13's golden set.
func (h *harness) stageGate(t *testing.T, task runsapi.TaskResponse) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	runID, err := uuid.Parse(task.LatestRunID)
	if err != nil {
		t.Fatal(err)
	}
	// The wall deadline is rewritten off the DATABASE clock deliberately. Every
	// deciding predicate reads now() in the statement that mutates, while this
	// harness's app clock is pinned to a fixed date in 2026 — so a run left with
	// the deadline intake computed would be refused as expired on any day after
	// it, and every assertion below would be about the wrong refusal.
	var baseSHA string
	if err := h.pool.QueryRow(ctx,
		`UPDATE runs SET status = 'waiting', started_at = now(), wall_deadline = now() + interval '1 hour'
		 WHERE id = $1 RETURNING base_sha`,
		runID).Scan(&baseSHA); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO artifacts (id, run_id, node_id, attempt, generation, type, payload, parent_ids, created_at)
		 VALUES (uuidv7(), $1, 'impl', 1, 1, 'patch',
		         jsonb_build_object('tree_hash', $2::text, 'base_sha', $3::text), '[]'::jsonb, now())`,
		runID, stagedTreeHash, baseSHA); err != nil {
		t.Fatal(err)
	}
	// The finished phase that produced the patch is staged too. It is what a
	// real gate is always preceded by, and it is the only undecided phase the
	// projection has to leave alone — without it, "a decision lands on the row
	// it was made on" has nothing to be wrong about.
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, generation, finished_at, created_at)
		 VALUES ($1, 'impl', 1, 3, 'succeeded', 1, now(), now())`, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, generation, created_at)
		 VALUES ($1, 'gate', 1, 3, 'waiting_decision', 1, now())`, runID); err != nil {
		t.Fatal(err)
	}
	return runID
}

// gatedRun is createTask + stageGate: a run of the caller's, sitting at its gate.
func (h *harness) gatedRun(t *testing.T, token string, repoID uuid.UUID, title string) (runsapi.TaskResponse, uuid.UUID) {
	t.Helper()
	task := h.createTaskInRepo(t, token, repoID, title)
	return task, h.stageGate(t, task)
}

func decisionPath(runID uuid.UUID, node string) string {
	return "/api/v1/runs/" + runID.String() + "/gates/" + node + "/decision"
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

func TestAPI_GateDecisionRequiresApproveGrant(t *testing.T) {
	h := newHarness(t)
	// The runner requests the task and the approver decides it, so nothing
	// here depends on the shipped policy permitting self-approval.
	_, runID := h.gatedRun(t, "runner-tok", h.repoID, "ship it")
	body := runsapi.GateDecisionRequest{Decision: "approve"}

	t.Run("a run grant may not decide", func(t *testing.T) {
		resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "runner-tok", body)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("run grant decided a gate: status %d, want 403", resp.StatusCode)
		}
		if pd := decodeInto[httpx.ProblemDetails](t, resp); pd.Detail != "insufficient project grant" {
			t.Errorf("detail = %q, want the grant message", pd.Detail)
		}
	})

	t.Run("a view grant may not decide", func(t *testing.T) {
		if resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "viewer-tok", body); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("view grant decided a gate: status %d, want 403", resp.StatusCode)
		}
	})

	t.Run("an approve grant decides and gets the decision back", func(t *testing.T) {
		resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "approver-tok", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("approve grant status %d, want 200", resp.StatusCode)
		}
		out := decodeInto[runsapi.GateDecisionResponse](t, resp)
		if out.Decision != "approve" {
			t.Errorf("decision = %q, want approve", out.Decision)
		}
		// The three fields that come from the service rather than from the
		// request body. Echoing the decision back proves only that the handler
		// can copy a string.
		if out.DecisionID == "" {
			t.Error("decision_id is empty; the decision row's id must reach the caller")
		}
		if out.RunStatus != string(runs.RunRunning) {
			t.Errorf("run_status = %q, want running: an approval resumes the run", out.RunStatus)
		}
		if out.Generation != 1 {
			t.Errorf("generation = %d, want 1: approve reports the generation decided", out.Generation)
		}
		// The gate dominates a git-publish node, so the approval mints the
		// single-use authorization that node will spend.
		if out.AuthorizationID == "" {
			t.Error("authorization_id is empty; an approval over a publish must surface its token")
		}
	})
}

func TestAPI_GateDecisionOnAnUnknownRunIs404AndOnADecidedGateIs409(t *testing.T) {
	h := newHarness(t)
	_, runID := h.gatedRun(t, "approver-tok", h.repoID, "ship it")
	approve := runsapi.GateDecisionRequest{Decision: "approve"}

	t.Run("an unknown run is not found", func(t *testing.T) {
		for _, path := range []string{
			decisionPath(uuid.New(), "gate"),
			"/api/v1/runs/not-a-uuid/gates/gate/decision",
		} {
			if resp := h.do(t, http.MethodPost, path, "admin-tok", approve); resp.StatusCode != http.StatusNotFound {
				t.Errorf("POST %s = %d, want 404", path, resp.StatusCode)
			}
		}
	})

	t.Run("a node that is not a gate awaiting a decision is a conflict", func(t *testing.T) {
		resp := h.do(t, http.MethodPost, decisionPath(runID, "nosuchnode"), "approver-tok", approve)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("unknown node status %d, want 409 (the node is not a gate awaiting a decision)", resp.StatusCode)
		}
	})

	t.Run("the first decision wins and the second is a conflict", func(t *testing.T) {
		if resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "approver-tok", approve); resp.StatusCode != http.StatusOK {
			t.Fatalf("first decision = %d, want 200", resp.StatusCode)
		}
		resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "reject"})
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("second decision status %d, want 409", resp.StatusCode)
		}
	})
}

func TestAPI_GateDecisionValidatesItsBody(t *testing.T) {
	h := newHarness(t)
	_, runID := h.gatedRun(t, "approver-tok", h.repoID, "ship it")

	for _, c := range []struct {
		name  string
		body  runsapi.GateDecisionRequest
		field string
	}{
		{"unknown decision", runsapi.GateDecisionRequest{Decision: "maybe"}, "decision"},
		{"empty decision", runsapi.GateDecisionRequest{}, "decision"},
		{"request_changes without a note", runsapi.GateDecisionRequest{Decision: "request_changes"}, "note"},
		{"oversize note", runsapi.GateDecisionRequest{Decision: "approve", Note: strings.Repeat("x", 9000)}, "note"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "approver-tok", c.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", resp.StatusCode)
			}
			pd := decodeInto[httpx.ProblemDetails](t, resp)
			if len(pd.Errors) == 0 {
				t.Fatal("a validation failure must name its field")
			}
			blamed := false
			for _, fe := range pd.Errors {
				if fe.Field == c.field {
					blamed = true
				}
			}
			if !blamed {
				t.Errorf("errors = %+v, want one blaming %q", pd.Errors, c.field)
			}
		})
	}

	// Validation refuses before the gate is touched, so the gate is still
	// decidable afterwards.
	if resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "approver-tok",
		runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("decision after four refused bodies = %d, want 200", resp.StatusCode)
	}
}

func TestAPI_ApprovalsInboxIsScopedToGrants(t *testing.T) {
	h := newHarness(t)
	taskA, runA := h.gatedRun(t, "approver-tok", h.repoID, "gate on A")
	taskB, runB := h.gatedRun(t, "approver-b-tok", h.repoB, "gate on B")

	inbox := func(t *testing.T, token string) runsapi.ApprovalListResponse {
		t.Helper()
		resp := h.do(t, http.MethodGet, "/api/v1/approvals", token, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("inbox as %s = %d, want 200", token, resp.StatusCode)
		}
		return decodeInto[runsapi.ApprovalListResponse](t, resp)
	}
	runIDs := func(list runsapi.ApprovalListResponse) []string {
		out := make([]string, 0, len(list.Approvals))
		for _, a := range list.Approvals {
			out = append(out, a.RunID)
		}
		return out
	}
	onlyRun := func(t *testing.T, token string, want uuid.UUID) runsapi.ApprovalResponse {
		t.Helper()
		list := inbox(t, token)
		if len(list.Approvals) != 1 || list.Approvals[0].RunID != want.String() {
			t.Fatalf("%s's inbox = %v, want exactly %s", token, runIDs(list), want)
		}
		return list.Approvals[0]
	}

	// Both projects have a gate waiting, so "B does not see A's" is a statement
	// about grants and not about there being nothing to see.
	t.Run("each approver sees exactly their own project's gate", func(t *testing.T) {
		onlyRun(t, "approver-tok", runA)
		onlyRun(t, "approver-b-tok", runB)
	})

	t.Run("the inbox shows what a caller can see, not what they may approve", func(t *testing.T) {
		// A view-only member of project A sees A's gate. That is the design:
		// the inbox is a read, and the decision endpoint is what enforces
		// approve. Deleting this case is how the decision gets lost.
		onlyRun(t, "viewer-tok", runA)
		if resp := h.do(t, http.MethodPost, decisionPath(runA, "gate"), "viewer-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("viewer decided the gate they can see: %d, want 403", resp.StatusCode)
		}
	})

	t.Run("an admin sees every waiting gate", func(t *testing.T) {
		list := inbox(t, "admin-tok")
		if len(list.Approvals) != 2 {
			t.Fatalf("admin inbox = %v, want both gates", runIDs(list))
		}
	})

	t.Run("a member with no grants has an empty inbox", func(t *testing.T) {
		if list := inbox(t, "outsider-tok"); len(list.Approvals) != 0 {
			t.Fatalf("ungranted inbox = %v, want none", runIDs(list))
		}
	})

	t.Run("an entry carries everything a list view renders", func(t *testing.T) {
		got := onlyRun(t, "approver-tok", runA)
		want := runsapi.ApprovalResponse{
			RunID: runA.String(), TaskID: taskA.ID, ProjectID: h.projectID.String(),
			TaskTitle: "gate on A", NodeID: "gate", Attempt: 1, Generation: 1,
			RequesterID: h.approver.UserID.String(), WaitingSince: got.WaitingSince,
		}
		if got != want {
			t.Errorf("entry = %+v, want %+v", got, want)
		}
		if got.WaitingSince.IsZero() {
			t.Error("waiting_since is zero; an inbox sorts and ages on it")
		}
		// B's entry names B's project, so the projection is not echoing one
		// project id at every caller.
		if b := onlyRun(t, "approver-b-tok", runB); b.ProjectID != h.projectB.String() || b.TaskID != taskB.ID {
			t.Errorf("B's entry = %+v, want project %s task %s", b, h.projectB, taskB.ID)
		}
	})
}

// stagePublication writes the intent a publish node creates when it spends an
// authorization, so the projection has a publication to carry. The receipts
// stay NULL: a pending intent is what the columns look like before anything
// has landed on the forge.
func (h *harness) stagePublication(t *testing.T, runID, authID uuid.UUID) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(),
		`INSERT INTO publish_intents (run_id, node_id, attempt, authorization_id, repo_id,
		                              forge_kind, repo_slug, credential_id, branch, target_branch,
		                              base_sha, tree_hash, title, body, author_email,
		                              commit_message, status, created_at, updated_at)
		 SELECT $1, 'publish', 1, $2, r.id, r.forge_kind, r.slug, r.credential_id,
		        'caesar/run', r.default_branch, (SELECT base_sha FROM runs WHERE id = $1), $3,
		        'a title', 'a body', 'approver@example.com', 'a commit', 'pending', now(), now()
		 FROM repos r WHERE r.id = $4`,
		runID, authID, stagedTreeHash, h.repoID); err != nil {
		t.Fatal(err)
	}
}

// The run projection carries the fields the gate surface added: the run's
// generation, each phase's generation and its decision, each artifact's
// generation, and the run's publications.
//
// It asserts the SHAPE over staged rows, deliberately not the conveyor arc.
// Two generations, request_changes followed by approve, and a publication that
// actually reaches 'published' on a forge belong to Task 13's
// TestGoldenSet_FullConveyorThroughTheAPI, which drives the real thing; there
// is no value in a second, fake version of it here.
func TestAPI_RunProjectionCarriesTheGateFields(t *testing.T) {
	h := newHarness(t)
	// The runner requests, the approver decides: self_approval is then a
	// projected fact rather than a constant this fixture cannot help but be.
	task, runID := h.gatedRun(t, "runner-tok", h.repoID, "ship it")
	var patchID string
	if err := h.pool.QueryRow(context.Background(),
		`SELECT id::text FROM artifacts WHERE run_id = $1 AND node_id = 'impl' AND type = 'patch'`,
		runID).Scan(&patchID); err != nil {
		t.Fatal(err)
	}

	resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "approver-tok",
		runsapi.GateDecisionRequest{Decision: "approve", Note: "ship it"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decision = %d, want 200", resp.StatusCode)
	}
	authID, err := uuid.Parse(decodeInto[runsapi.GateDecisionResponse](t, resp).AuthorizationID)
	if err != nil {
		t.Fatal(err)
	}
	h.stagePublication(t, runID, authID)

	getRun := func(t *testing.T, id uuid.UUID) runsapi.RunResponse {
		t.Helper()
		resp := h.do(t, http.MethodGet, "/api/v1/runs/"+id.String(), "viewer-tok", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get run = %d, want 200", resp.StatusCode)
		}
		return decodeInto[runsapi.RunResponse](t, resp)
	}
	run := getRun(t, runID)

	t.Run("the run and its rows carry their generation", func(t *testing.T) {
		if run.Generation != 1 {
			t.Errorf("run generation = %d, want 1", run.Generation)
		}
		gate := phaseByNodeID(t, run, "gate")
		if gate.Generation != 1 {
			t.Errorf("gate phase generation = %d, want 1", gate.Generation)
		}
		if len(run.Artifacts) != 1 || run.Artifacts[0].Generation != 1 {
			t.Errorf("artifacts = %+v, want one at generation 1", run.Artifacts)
		}
	})

	t.Run("the decided gate phase carries who decided and what they examined", func(t *testing.T) {
		gate := phaseByNodeID(t, run, "gate")
		if gate.Decision == nil {
			t.Fatal("the decided gate phase carries no decision")
		}
		want := runsapi.GateDecisionSummary{
			Decision: "approve", Note: "ship it",
			ActedByID: h.approver.UserID.String(), ActedByEmail: "approver@example.com",
			ActedAt: gate.Decision.ActedAt, SelfApproval: false, ExaminedArtifactID: patchID,
		}
		if *gate.Decision != want {
			t.Errorf("decision = %+v, want %+v", *gate.Decision, want)
		}
		if gate.Decision.ActedAt.IsZero() {
			t.Error("acted_at is zero")
		}
		// The decision attaches to the phase row it was made on and to no
		// other. Counting is what catches a join keyed on anything else: after
		// a revision both gate attempts share a node id, and a decision
		// smeared across them reads as two verdicts nobody gave.
		decided := 0
		for _, p := range run.Phases {
			if p.Decision != nil {
				decided++
			}
		}
		if decided != 1 {
			t.Errorf("phases carrying a decision = %d of %d, want exactly the decided gate",
				decided, len(run.Phases))
		}
		if impl := phaseByNodeID(t, run, "impl"); impl.Decision != nil {
			t.Errorf("the undecided impl phase carries a decision: %+v", impl.Decision)
		}
	})

	t.Run("a pending publication projects its plan and no receipts", func(t *testing.T) {
		if len(run.Publications) != 1 {
			t.Fatalf("publications = %+v, want one", run.Publications)
		}
		pub := run.Publications[0]
		want := runsapi.PublicationResponse{
			NodeID: "publish", Status: "pending", Branch: "caesar/run",
			TargetBranch: "main", BaseSHA: run.BaseSHA,
		}
		if pub != want {
			t.Errorf("publication = %+v, want %+v (receipts are NULL until they land)", pub, want)
		}
	})

	t.Run("receipts reach the projection once they land", func(t *testing.T) {
		if _, err := h.pool.Exec(context.Background(),
			`UPDATE publish_intents SET status = 'published', head_sha = $2, pr_number = $3, pr_url = $4
			 WHERE run_id = $1`,
			runID, "abc123", 42, "https://forge.test/pr/42"); err != nil {
			t.Fatal(err)
		}
		pub := getRun(t, runID).Publications[0]
		if pub.Status != "published" || pub.HeadSHA != "abc123" || pub.PRNumber != 42 ||
			pub.PRURL != "https://forge.test/pr/42" {
			t.Errorf("publication = %+v, want the receipts", pub)
		}
	})

	t.Run("a halted publication carries its reason", func(t *testing.T) {
		// Rewound to 'pushed' first: halted is a one-way exit from an
		// unfinished state, so this exercises the projection on a shape the
		// state machine can actually produce.
		if _, err := h.pool.Exec(context.Background(),
			`UPDATE publish_intents SET status = 'pushed', pr_number = NULL, pr_url = NULL WHERE run_id = $1`,
			runID); err != nil {
			t.Fatal(err)
		}
		if _, err := h.pool.Exec(context.Background(),
			`UPDATE publish_intents SET status = 'halted', halt_reason = $2 WHERE run_id = $1`,
			runID, "the branch vanished from the forge"); err != nil {
			t.Fatal(err)
		}
		pub := getRun(t, runID).Publications[0]
		if pub.Status != "halted" || pub.HaltReason != "the branch vanished from the forge" {
			t.Errorf("publication = %+v, want the halt and its reason", pub)
		}
	})

	t.Run("a self-approval is projected as one", func(t *testing.T) {
		// The requester decides their own gate. The shipped policy permits it
		// and marks the row distinctly (spec §6); the marker is worth nothing
		// if it stops at the database.
		_, ownRun := h.gatedRun(t, "approver-tok", h.repoID, "my own gate")
		if resp := h.do(t, http.MethodPost, decisionPath(ownRun, "gate"), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusOK {
			t.Fatalf("self-approval = %d, want 200", resp.StatusCode)
		}
		gate := phaseByNodeID(t, getRun(t, ownRun), "gate")
		if gate.Decision == nil || !gate.Decision.SelfApproval {
			t.Errorf("decision = %+v, want self_approval true", gate.Decision)
		}
	})

	t.Run("a run list entry stays shallow", func(t *testing.T) {
		resp := h.do(t, http.MethodGet, "/api/v1/tasks/"+task.ID+"/runs", "viewer-tok", nil)
		list := decodeInto[runsapi.RunListResponse](t, resp)
		if len(list.Runs) != 1 {
			t.Fatalf("runs = %+v, want one", list.Runs)
		}
		if len(list.Runs[0].Publications) != 0 {
			t.Errorf("list entries must stay shallow: %+v", list.Runs[0].Publications)
		}
	})
}

func tryPhaseByNodeID(run runsapi.RunResponse, nodeID string) (runsapi.PhaseResponse, bool) {
	for _, p := range run.Phases {
		if p.NodeID == nodeID {
			return p, true
		}
	}
	return runsapi.PhaseResponse{}, false
}

func phaseByNodeID(t *testing.T, run runsapi.RunResponse, nodeID string) runsapi.PhaseResponse {
	t.Helper()
	p, ok := tryPhaseByNodeID(run, nodeID)
	if !ok {
		t.Fatalf("no phase for node %q in %+v", nodeID, run.Phases)
	}
	return p
}
