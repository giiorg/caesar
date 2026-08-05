//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	assetsapi "github.com/giiorg/caesar/backend/internal/assets/api"
	identityapi "github.com/giiorg/caesar/backend/internal/identity/api"
	workflowsapi "github.com/giiorg/caesar/backend/internal/workflows/api"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs"
	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// goldenClient drives the API as one logged-in human.
type goldenClient struct {
	srv   *httptest.Server
	token string
}

func goldenLogin(t *testing.T, srv *httptest.Server, email, password string) goldenClient {
	t.Helper()
	body, _ := json.Marshal(identityapi.LoginRequest{Email: email, Password: password})
	resp, err := http.Post(srv.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s = %d", email, resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == identityapi.SessionCookie {
			return goldenClient{srv: srv, token: c.Value}
		}
	}
	t.Fatalf("login %s: no session cookie", email)
	return goldenClient{}
}

// do sends payload (nil for none), decodes into out on 2xx (nil to skip),
// and returns the status code.
func (c goldenClient) do(t *testing.T, method, path string, payload, out any) int {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Cookie", identityapi.SessionCookie+"="+c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

// must fails the test unless got matches want.
func must(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %d, want %d", what, got, want)
	}
}

// The golden-set seed (spec §7): the month-one loop minus gates, against
// real services and real routes with the fake executor and forge. Plan 08
// extends this exact test with gates and publishing.
func TestGoldenSet_MonthOneLoopThroughRealRoutes(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	headSHA, err := fake.InitBareRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

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
	workflowsSvc := workflows.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}))

	// The runs service takes the production adapters but the fake forge
	// and executor: deterministic head resolution and scripted phases.
	heads := map[uuid.UUID]string{}
	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		fake.Forge{Heads: heads}, runs.Params{Snapshot: runs.DefaultSnapshot()})

	ops := identityapi.Operations(identityapi.New(identitySvc, 90*24*time.Hour, assetsSvc))
	ops = append(ops, assetsapi.Operations(assetsapi.New(assetsSvc, identitySvc, identitySvc, identitySvc))...)
	ops = append(ops, workflowsapi.Operations(workflowsapi.New(workflowsSvc, identitySvc))...)
	ops = append(ops, runsapi.Operations(runsapi.New(runsSvc, identitySvc, identitySvc, identitySvc))...)
	srv := httptest.NewServer(Routes(discard(), pool, ops, "http://caesar.test"))
	t.Cleanup(srv.Close)

	orch := runs.NewOrchestrator(runsSvc, fake.NewExecutor(), runs.AlwaysAdmit{}, runs.OrchestratorConfig{
		Workers: 2, PollInterval: 20 * time.Millisecond, LeaseTTL: 30 * time.Second,
		HeartbeatInterval: 10 * time.Second, OutboxRetention: 168 * time.Hour,
	}, discard())
	orchCtx, orchCancel := context.WithCancel(ctx)
	orchDone := make(chan struct{})
	go func() { defer close(orchDone); _ = orch.Run(orchCtx) }()
	t.Cleanup(func() { orchCancel(); <-orchDone })

	// The admin sets the stage: project, credentials, harness, repo,
	// workflow + document, member with a run grant.
	admin := goldenLogin(t, srv, "root@example.com", password)

	var project assetsapi.ProjectResponse
	must(t, "create project", admin.do(t, http.MethodPost, "/api/v1/projects",
		assetsapi.CreateProjectRequest{Name: "golden"}, &project), http.StatusCreated)

	var modelCred, forgeCred assetsapi.CredentialResponse
	must(t, "create model credential", admin.do(t, http.MethodPost, "/api/v1/credentials",
		assetsapi.CreateCredentialRequest{Name: "anthropic", Kind: "model_key", Secret: "sk-ant-golden"}, &modelCred), http.StatusCreated)
	must(t, "create forge credential", admin.do(t, http.MethodPost, "/api/v1/credentials",
		assetsapi.CreateCredentialRequest{Name: "github-bot", Kind: "forge_token", Secret: "ghp-golden"}, &forgeCred), http.StatusCreated)

	var harness assetsapi.HarnessResponse
	must(t, "register harness", admin.do(t, http.MethodPost, "/api/v1/harnesses",
		assetsapi.RegisterHarnessRequest{
			Name: "cc", Kind: "claude-code",
			ImageRef:         "ghcr.io/giiorg/caesar-harness-claude@sha256:abc",
			ProviderEndpoint: "https://api.anthropic.com",
			CredentialID:     modelCred.ID,
		}, &harness), http.StatusCreated)

	var repo assetsapi.RepoResponse
	must(t, "create repo", admin.do(t, http.MethodPost, "/api/v1/projects/"+project.ID+"/repos",
		assetsapi.CreateRepoRequest{Forge: "github", Slug: "golden/seed", DefaultBranch: "main", CredentialID: forgeCred.ID}, &repo), http.StatusCreated)
	repoID, err := uuid.Parse(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	heads[repoID] = headSHA

	var wf workflowsapi.WorkflowResponse
	must(t, "create workflow", admin.do(t, http.MethodPost, "/api/v1/workflows",
		workflowsapi.CreateWorkflowRequest{Name: "golden-loop"}, &wf), http.StatusCreated)

	doc := `{
	  "schema": 1,
	  "nodes": [
	    {"id": "plan", "type": "plan", "harness": "cc", "instructions": "Plan the change."},
	    {"id": "impl", "type": "implement", "harness": "cc", "instructions": "Implement the plan."},
	    {"id": "review", "type": "review", "harness": "cc", "instructions": "Review the patch."}
	  ],
	  "edges": [
	    {"from": "plan", "to": "impl", "artifact": "plan"},
	    {"from": "impl", "to": "review", "artifact": "patch"}
	  ]
	}`
	var version workflowsapi.WorkflowVersionResponse
	must(t, "create version", admin.do(t, http.MethodPost, "/api/v1/workflows/"+wf.ID+"/versions",
		workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(doc)}, &version), http.StatusCreated)

	var member identityapi.UserResponse
	must(t, "create member", admin.do(t, http.MethodPost, "/api/v1/users",
		identityapi.CreateUserRequest{Email: "member@example.com", Password: password, Role: "member"}, &member), http.StatusCreated)
	must(t, "grant run", admin.do(t, http.MethodPut, "/api/v1/projects/"+project.ID+"/grants",
		identityapi.SetGrantRequest{UserID: member.ID, Level: "run"}, nil), http.StatusNoContent)

	// The member launches the task and watches the run to success.
	memberClient := goldenLogin(t, srv, "member@example.com", password)
	var task runsapi.TaskResponse
	must(t, "create task", memberClient.do(t, http.MethodPost, "/api/v1/tasks",
		runsapi.CreateTaskRequest{RepoID: repo.ID, WorkflowID: wf.ID, WorkflowVersion: 1, Title: "Golden seed"}, &task), http.StatusCreated)

	var run runsapi.RunResponse
	deadline := time.Now().Add(15 * time.Second)
	for {
		must(t, "get run", memberClient.do(t, http.MethodGet, "/api/v1/runs/"+task.LatestRunID, nil, &run), http.StatusOK)
		if run.Status == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not succeed within 15s; status = %s, outcome = %+v", run.Status, run.Outcome)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The run pinned what the plan promised.
	if run.BaseSHA != headSHA {
		t.Errorf("run base_sha = %s, want %s", run.BaseSHA, headSHA)
	}
	if run.DefinitionHash != version.ContentHash {
		t.Errorf("run definition_hash = %s, want the version's content_hash %s", run.DefinitionHash, version.ContentHash)
	}

	// Three phases, all first-attempt successes.
	if len(run.Phases) != 3 {
		t.Fatalf("phases = %d, want 3", len(run.Phases))
	}
	for _, p := range run.Phases {
		if p.Status != "succeeded" || p.Attempt != 1 {
			t.Errorf("phase %s = %s attempt %d, want succeeded attempt 1", p.NodeID, p.Status, p.Attempt)
		}
	}

	// Artifact lineage: plan → patch → verdict.
	byType := map[string]runsapi.ArtifactResponse{}
	for _, a := range run.Artifacts {
		byType[a.Type] = a
	}
	if len(run.Artifacts) != 3 {
		t.Fatalf("artifacts = %+v, want plan, patch, review_verdict", run.Artifacts)
	}
	plan, patch, verdict := byType["plan"], byType["patch"], byType["review_verdict"]
	if len(patch.ParentIDs) != 1 || patch.ParentIDs[0] != plan.ID {
		t.Errorf("patch parents = %v, want [%s] (the plan)", patch.ParentIDs, plan.ID)
	}
	if len(verdict.ParentIDs) != 1 || verdict.ParentIDs[0] != patch.ID {
		t.Errorf("verdict parents = %v, want [%s] (the patch)", verdict.ParentIDs, patch.ID)
	}

	// The patch payload carries the pinned base SHA (payloads are not on
	// the API surface; read the row).
	runID, err := uuid.Parse(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rawPayload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM artifacts WHERE run_id = $1 AND type = 'patch'`, runID).Scan(&rawPayload); err != nil {
		t.Fatal(err)
	}
	var patchPayload map[string]any
	if err := json.Unmarshal(rawPayload, &patchPayload); err != nil {
		t.Fatal(err)
	}
	if patchPayload["base_sha"] != headSHA {
		t.Errorf("patch payload base_sha = %v, want %s", patchPayload["base_sha"], headSHA)
	}

	// Events: strictly ascending ids, created…succeeded bookends, and the
	// poll cursor drains to empty.
	var events runsapi.RunEventsResponse
	must(t, "list events", memberClient.do(t, http.MethodGet, "/api/v1/runs/"+run.ID+"/events", nil, &events), http.StatusOK)
	if len(events.Events) < 2 {
		t.Fatalf("events = %d, want at least the created/succeeded bookends", len(events.Events))
	}
	for i := 1; i < len(events.Events); i++ {
		if events.Events[i].ID <= events.Events[i-1].ID {
			t.Errorf("event ids not strictly ascending: %d after %d", events.Events[i].ID, events.Events[i-1].ID)
		}
	}
	first, last := events.Events[0], events.Events[len(events.Events)-1]
	if first.Kind != "run.created" || last.Kind != "run.succeeded" {
		t.Errorf("event bookends = %s … %s, want run.created … run.succeeded", first.Kind, last.Kind)
	}
	var afterLast runsapi.RunEventsResponse
	must(t, "list events after last", memberClient.do(t, http.MethodGet,
		"/api/v1/runs/"+run.ID+"/events?after="+strconv.FormatInt(last.ID, 10), nil, &afterLast), http.StatusOK)
	if len(afterLast.Events) != 0 {
		t.Errorf("events after the last id = %d, want 0", len(afterLast.Events))
	}

	// Audit: the member requested; the system finished. One outbox row
	// per run event.
	memberID, err := uuid.Parse(member.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"task.created", "run.created"} {
		var actor uuid.NullUUID
		if err := pool.QueryRow(ctx, `SELECT actor_user_id FROM audit_entries WHERE action = $1`, action).Scan(&actor); err != nil {
			t.Fatalf("audit %s: %v", action, err)
		}
		if !actor.Valid || actor.UUID != memberID {
			t.Errorf("audit %s actor = %v, want the member %s", action, actor, memberID)
		}
	}
	var finishedActor uuid.NullUUID
	if err := pool.QueryRow(ctx, `SELECT actor_user_id FROM audit_entries WHERE action = 'run.finished'`).Scan(&finishedActor); err != nil {
		t.Fatal(err)
	}
	if finishedActor.Valid {
		t.Errorf("audit run.finished actor = %s, want NULL (system)", finishedActor.UUID)
	}
	var eventRows, outboxRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM run_events`).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if eventRows == 0 || outboxRows != eventRows {
		t.Errorf("outbox rows = %d, run_events rows = %d, want equal and nonzero", outboxRows, eventRows)
	}

	// A stranger with no grant can neither launch nor see anything.
	var stranger identityapi.UserResponse
	must(t, "create stranger", admin.do(t, http.MethodPost, "/api/v1/users",
		identityapi.CreateUserRequest{Email: "stranger@example.com", Password: password, Role: "member"}, &stranger), http.StatusCreated)
	strangerClient := goldenLogin(t, srv, "stranger@example.com", password)
	must(t, "stranger create task", strangerClient.do(t, http.MethodPost, "/api/v1/tasks",
		runsapi.CreateTaskRequest{RepoID: repo.ID, WorkflowID: wf.ID, WorkflowVersion: 1, Title: "Not allowed"}, nil), http.StatusForbidden)
	var visible runsapi.TaskListResponse
	must(t, "stranger list tasks", strangerClient.do(t, http.MethodGet, "/api/v1/tasks", nil, &visible), http.StatusOK)
	if len(visible.Tasks) != 0 {
		t.Errorf("stranger sees %d tasks, want 0", len(visible.Tasks))
	}
}
