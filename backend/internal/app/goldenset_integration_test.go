//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	"github.com/giiorg/caesar/backend/internal/webui"
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
	forge := fake.NewForge(map[uuid.UUID]string{})
	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		forge, fake.NewPublisher(nil), principalSource{svc: identitySvc},
		runs.Params{
			Snapshot: runs.DefaultSnapshot(),
			Labels:   runsLabelSource{assets: assetsSvc, workflows: workflowsSvc},
		})

	ops := identityapi.Operations(identityapi.New(identitySvc, 90*24*time.Hour, assetsSvc))
	ops = append(ops, assetsapi.Operations(assetsapi.New(assetsSvc, identitySvc, identitySvc, identitySvc))...)
	ops = append(ops, workflowsapi.Operations(workflowsapi.New(workflowsSvc, identitySvc))...)
	ops = append(ops, runsapi.Operations(runsapi.New(runsSvc, identitySvc, identitySvc, identitySvc, nil))...)
	srv := httptest.NewServer(Routes(discard(), pool, ops, "http://caesar.test", webui.Handler()))
	t.Cleanup(srv.Close)

	orch := runs.NewOrchestrator(runsSvc, fake.NewExecutor(), runs.AlwaysAdmit{}, nil, runs.OrchestratorConfig{
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
	forge.Head(repoID, headSHA)

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

// ---------------------------------------------------------------------------
// The golden set proper (spec §7): the whole conveyor — task → phases → gate →
// approve → publish-with-expected-SHA — driven through the real HTTP API, with
// crashes injected around every publish step.
//
// The seed test above stays as it is: it drives a GATELESS workflow against the
// executor's synthetic patch, which is a different shape from the publishable
// one this fixture needs, and it is the only test that covers that shape.
// ---------------------------------------------------------------------------

const goldenPassword = "a long enough password"

// goldenImplNode is the conveyor's implement node id. fake/executor.go names
// the file a publishable patch creates per node id ("CAESAR-"+nodeID+".md") so
// two implement nodes in one definition cannot both claim the same path, so
// this constant is what the remote-content assertion is derived from.
const goldenImplNode = "impl"

// goldenConveyorDoc is the full loop: plan → implement → review → gate →
// git-publish, with the gate rewinding to the implement node for at most two
// rounds. The gate takes two inbound edges — the patch it examines and the
// review verdict — and passes the patch through to publish, which is what
// validateGatePassThrough requires.
const goldenConveyorDoc = `{
  "schema": 1,
  "nodes": [
    {"id": "plan", "type": "plan", "harness": "cc", "instructions": "Plan the change."},
    {"id": "impl", "type": "implement", "harness": "cc", "instructions": "Implement the plan."},
    {"id": "review", "type": "review", "harness": "cc", "instructions": "Review the patch."},
    {"id": "gate", "type": "gate", "on_request_changes": {"target": "impl", "max_rounds": 2}},
    {"id": "publish", "type": "git-publish"}
  ],
  "edges": [
    {"from": "plan", "to": "impl", "artifact": "plan"},
    {"from": "impl", "to": "review", "artifact": "patch"},
    {"from": "impl", "to": "gate", "artifact": "patch"},
    {"from": "review", "to": "gate", "artifact": "review_verdict"},
    {"from": "gate", "to": "publish", "artifact": "patch"}
  ]
}`

// goldenEnv is the product behind its real routes, plus the three seams a
// crash test has to own: the publisher's and forge's crash switches, and the
// statement-fault tracer on the pool. Everything a test needs is a METHOD on
// this type — package app compiles the integration, property and
// runnerintegration files together under `go vet`, so a package-level helper
// here would collide with one over there and break `backend:ci` while every
// targeted run stayed green.
type goldenEnv struct {
	pool      *pgxpool.Pool
	faults    *testdb.QueryFaults
	srv       *httptest.Server
	forge     *fake.Forge
	publisher *fake.Publisher
	// bare is the real repository the publication pushes to; baseSHA is its
	// seed commit, which is also the run's pinned base.
	bare       string
	baseSHA    string
	repoID     string
	workflowID string
}

// newGoldenEnv stages the world an approver-gated publishing conveyor needs:
// project, credentials, harness, a repo backed by a real bare git repository,
// the conveyor document at version 1, and two members — requester@example.com
// with run, approver@example.com with approve.
func newGoldenEnv(t *testing.T) *goldenEnv {
	t.Helper()
	ctx := context.Background()
	pool, faults := testdb.NewWithFaults(t)

	dir := t.TempDir()
	baseSHA, err := fake.InitBareRepo(dir)
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(dir, "bare.git")

	identitySvc := identity.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	if _, err := identitySvc.Bootstrap(ctx, "root@example.com", goldenPassword); err != nil {
		t.Fatal(err)
	}
	keys, err := secrets.ParseKeyset("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	assetsSvc := assets.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}), keys)
	workflowsSvc := workflows.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}))

	forge := fake.NewForge(map[uuid.UUID]string{})
	publisher := fake.NewPublisher(nil)
	executor := fake.NewExecutor()
	runsSvc := runs.NewService(pool, clock.System{}, audit.NewRecorder(clock.System{}),
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		forge, publisher, principalSource{svc: identitySvc},
		runs.Params{
			Snapshot: runs.DefaultSnapshot(),
			Labels:   runsLabelSource{assets: assetsSvc, workflows: workflowsSvc},
		})

	ops := identityapi.Operations(identityapi.New(identitySvc, 90*24*time.Hour, assetsSvc))
	ops = append(ops, assetsapi.Operations(assetsapi.New(assetsSvc, identitySvc, identitySvc, identitySvc))...)
	ops = append(ops, workflowsapi.Operations(workflowsapi.New(workflowsSvc, identitySvc))...)
	ops = append(ops, runsapi.Operations(runsapi.New(runsSvc, identitySvc, identitySvc, identitySvc, nil))...)
	srv := httptest.NewServer(Routes(discard(), pool, ops, "http://caesar.test", webui.Handler()))
	t.Cleanup(srv.Close)

	orch := runs.NewOrchestrator(runsSvc, executor, runs.AlwaysAdmit{}, nil, runs.OrchestratorConfig{
		Workers: 2, PollInterval: 20 * time.Millisecond, LeaseTTL: 30 * time.Second,
		HeartbeatInterval: 10 * time.Second, OutboxRetention: 168 * time.Hour,
	}, discard())
	orchCtx, orchCancel := context.WithCancel(ctx)
	orchDone := make(chan struct{})
	go func() { defer close(orchDone); _ = orch.Run(orchCtx) }()
	t.Cleanup(func() { orchCancel(); <-orchDone })

	admin := goldenLogin(t, srv, "root@example.com", goldenPassword)

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
	// All four ports have to be told about the bare repository, not just the
	// head resolver: the forge probes branches and pull requests against it,
	// the publisher pushes to it, and the executor needs it to emit a
	// PUBLISHABLE patch — without it the implement node emits the synthetic
	// one, whose tree hash is a digest rather than a git tree object id, and
	// gitops.Publish refuses it.
	forge.Head(repoID, baseSHA)
	forge.Remote(repoID, bare)
	publisher.Remote(repoID, bare)
	executor.Remote(repoID, bare)

	var wf workflowsapi.WorkflowResponse
	must(t, "create workflow", admin.do(t, http.MethodPost, "/api/v1/workflows",
		workflowsapi.CreateWorkflowRequest{Name: "golden-conveyor"}, &wf), http.StatusCreated)
	var version workflowsapi.WorkflowVersionResponse
	must(t, "create version", admin.do(t, http.MethodPost, "/api/v1/workflows/"+wf.ID+"/versions",
		workflowsapi.CreateWorkflowVersionRequest{Document: json.RawMessage(goldenConveyorDoc)}, &version), http.StatusCreated)

	for _, m := range []struct{ email, level string }{
		{"requester@example.com", "run"},
		{"approver@example.com", "approve"},
	} {
		var user identityapi.UserResponse
		must(t, "create "+m.email, admin.do(t, http.MethodPost, "/api/v1/users",
			identityapi.CreateUserRequest{Email: m.email, Password: goldenPassword, Role: "member"}, &user), http.StatusCreated)
		must(t, "grant "+m.level, admin.do(t, http.MethodPut, "/api/v1/projects/"+project.ID+"/grants",
			identityapi.SetGrantRequest{UserID: user.ID, Level: m.level}, nil), http.StatusNoContent)
	}

	return &goldenEnv{
		pool: pool, faults: faults, srv: srv, forge: forge, publisher: publisher,
		bare: bare, baseSHA: baseSHA,
		repoID: repo.ID, workflowID: wf.ID,
	}
}

func (g *goldenEnv) createTask(t *testing.T, c goldenClient, title string) runsapi.TaskResponse {
	t.Helper()
	var task runsapi.TaskResponse
	must(t, "create task", c.do(t, http.MethodPost, "/api/v1/tasks", runsapi.CreateTaskRequest{
		RepoID: g.repoID, WorkflowID: g.workflowID, WorkflowVersion: 1,
		Title: title, Inputs: map[string]any{"topic": "changelog"},
	}, &task), http.StatusCreated)
	return task
}

// decide posts a gate decision and fails unless the API accepted it. The status
// is checked here rather than at the call site because the interesting refusal
// — a 409 for spent revision rounds or an already-settled publication — would
// otherwise surface forty lines later as a wrong generation count, pointing at
// the wrong thing entirely.
func (c goldenClient) decide(t *testing.T, runID, node, decision, note string) runsapi.GateDecisionResponse {
	t.Helper()
	var out runsapi.GateDecisionResponse
	if got := c.do(t, http.MethodPost, "/api/v1/runs/"+runID+"/gates/"+node+"/decision",
		runsapi.GateDecisionRequest{Decision: decision, Note: note}, &out); got != http.StatusOK {
		t.Fatalf("decide %s on gate %q = %d, want 200", decision, node, got)
	}
	return out
}

// waitUntil polls cond every 20ms, failing with what if it never holds.
func (g *goldenEnv) waitUntil(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %s", what, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForApproval blocks until the run's gate appears in the caller's approval
// inbox at the given generation — the surface a human actually watches. The
// generation is part of the wait because a revision leaves two waiting gates
// over the run's life, and matching on the run id alone would return from the
// first one twice.
func (g *goldenEnv) waitForApproval(t *testing.T, c goldenClient, runID string, generation int32) {
	t.Helper()
	g.waitUntil(t, "gate on run "+runID+" reaching the approval inbox at generation "+strconv.Itoa(int(generation)),
		30*time.Second, func() bool {
			var list runsapi.ApprovalListResponse
			must(t, "list approvals", c.do(t, http.MethodGet, "/api/v1/approvals", nil, &list), http.StatusOK)
			for _, a := range list.Approvals {
				if a.RunID == runID && a.Generation == generation {
					return true
				}
			}
			return false
		})
}

// waitForRun polls GET /api/v1/runs/{id} — the HTTP surface, deliberately not
// the service, which the package-level waitForRunStatus polls instead — until
// the run reports want, and returns that response.
func (g *goldenEnv) waitForRun(t *testing.T, c goldenClient, runID, want string, within time.Duration) runsapi.RunResponse {
	t.Helper()
	var run runsapi.RunResponse
	deadline := time.Now().Add(within)
	for {
		run = runsapi.RunResponse{}
		must(t, "get run", c.do(t, http.MethodGet, "/api/v1/runs/"+runID, nil, &run), http.StatusOK)
		if run.Status == want {
			return run
		}
		if run.Status == "succeeded" || run.Status == "failed" {
			t.Fatalf("run %s reached %s, want %s (outcome %+v)", runID, run.Status, want, run.Outcome)
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached %s within %s; status = %s, outcome = %+v", runID, want, within, run.Status, run.Outcome)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (g *goldenEnv) eventKinds(t *testing.T, c goldenClient, runID string) []string {
	t.Helper()
	events := g.events(t, c, runID)
	kinds := make([]string, len(events))
	for i, e := range events {
		kinds[i] = e.Kind
	}
	return kinds
}

// assertEventSubsequence checks that want appears in the run's event stream in
// order, with anything at all in between. It is a SUBSEQUENCE and says so: the
// stream also carries phase.started/phase.succeeded for every phase, so an
// exact match is unassertable and a helper that promised one would be a lie
// that outlived whoever wrote it. What this pins is the ORDER of the
// milestones, which is the property.
func (g *goldenEnv) assertEventSubsequence(t *testing.T, c goldenClient, runID string, want []string) {
	t.Helper()
	got := g.eventKinds(t, c, runID)
	i := 0
	for _, kind := range got {
		if i < len(want) && kind == want[i] {
			i++
		}
	}
	if i == len(want) {
		return
	}
	t.Errorf("event %q (#%d of the wanted subsequence) never arrived after the ones before it\nwant subsequence: %s\ngot stream (%d): %s",
		want[i], i+1, strings.Join(want, " "), len(got), strings.Join(got, " "))
}

func (g *goldenEnv) events(t *testing.T, c goldenClient, runID string) []runsapi.RunEventResponse {
	t.Helper()
	var events runsapi.RunEventsResponse
	must(t, "list events", c.do(t, http.MethodGet, "/api/v1/runs/"+runID+"/events", nil, &events), http.StatusOK)
	return events.Events
}

func (g *goldenEnv) countEventKind(t *testing.T, c goldenClient, runID, kind string) int {
	t.Helper()
	n := 0
	for _, k := range g.eventKinds(t, c, runID) {
		if k == kind {
			n++
		}
	}
	return n
}

// countEventsCarrying counts the run's events whose payload has a key. It
// exists for the pull request, which two different event kinds can announce:
// publish.pr_opened when the receipt lands on the first pass and
// publish.adopted when a recovered attempt finds the request already open
// (publish.go:915-917). "pr_number" is the key the source itself names as
// what tells an adopted pull request from an adopted branch, so it — not
// either kind — is what "exactly one pull request reached the human" counts.
func (g *goldenEnv) countEventsCarrying(t *testing.T, c goldenClient, runID, key string) int {
	t.Helper()
	n := 0
	for _, e := range g.events(t, c, runID) {
		if _, ok := e.Payload[key]; ok {
			n++
		}
	}
	return n
}

// countApprovals reports how many attention items the run still shows.
func (g *goldenEnv) countApprovals(t *testing.T, c goldenClient, runID string) int {
	t.Helper()
	var list runsapi.ApprovalListResponse
	must(t, "list approvals", c.do(t, http.MethodGet, "/api/v1/approvals", nil, &list), http.StatusOK)
	n := 0
	for _, a := range list.Approvals {
		if a.RunID == runID {
			n++
		}
	}
	return n
}

// count runs a counting query against the golden set's own pool. The audit and
// publishing tables are behind no read route, so the assertions that need them
// read the rows.
func (g *goldenEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := g.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// countAuditSubject scopes by action AND subject id, which is what makes the
// assertion about THIS run rather than about any run in the database.
func (g *goldenEnv) countAuditSubject(t *testing.T, action, subjectID string) int {
	t.Helper()
	return g.count(t, `SELECT count(*) FROM audit_entries WHERE action = $1 AND subject_id = $2`, action, subjectID)
}

// countAuditGateDecisions scopes gate.decided to a run. Its audit subject is
// the decision row, so the run only reaches it through gate_decisions.
func (g *goldenEnv) countAuditGateDecisions(t *testing.T, runID string) int {
	t.Helper()
	return g.count(t, `SELECT count(*) FROM audit_entries a
		 JOIN gate_decisions d ON d.id::text = a.subject_id
		 WHERE a.action = 'gate.decided' AND d.run_id = $1::uuid`, runID)
}

func (g *goldenEnv) countIntents(t *testing.T, runID string) int {
	t.Helper()
	return g.count(t, `SELECT count(*) FROM publish_intents WHERE run_id = $1::uuid`, runID)
}

func (g *goldenEnv) countAuthorizations(t *testing.T, runID, status string) int {
	t.Helper()
	return g.count(t, `SELECT count(*) FROM approved_patches WHERE run_id = $1::uuid AND status = $2`, runID, status)
}

// git runs a read-only command against the bare repository the publication
// really pushed to, returning its output and whether it succeeded. A failure
// is an answer here — "no such branch" is what an untouched remote looks like.
func (g *goldenEnv) git(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"--git-dir", g.bare}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

// remoteHead is the sha a branch points at on the remote, "" if there is no
// such branch.
func (g *goldenEnv) remoteHead(t *testing.T, branch string) string {
	t.Helper()
	out, ok := g.git(t, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if !ok {
		return ""
	}
	return out
}

// remoteFileAt is the content of path in the commit sha, "" if absent.
func (g *goldenEnv) remoteFileAt(t *testing.T, sha, path string) string {
	t.Helper()
	out, ok := g.git(t, "show", sha+":"+path)
	if !ok {
		return ""
	}
	return out
}

func (g *goldenEnv) remoteBranchCount(t *testing.T, prefix string) int {
	t.Helper()
	out, ok := g.git(t, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if !ok {
		t.Fatalf("listing remote branches: %s", out)
	}
	n := 0
	for _, ref := range strings.Fields(out) {
		if strings.HasPrefix(ref, prefix) {
			n++
		}
	}
	return n
}

func TestGoldenSet_FullConveyorThroughTheAPI(t *testing.T) {
	g := newGoldenEnv(t)
	requester := goldenLogin(t, g.srv, "requester@example.com", goldenPassword)
	approver := goldenLogin(t, g.srv, "approver@example.com", goldenPassword)

	task := g.createTask(t, requester, "Add a CHANGELOG")

	// One request-changes round, then approve. The generation in a decision
	// response means different things by decision — the NEW one a revision
	// opened, the one an approve decided — and both are read here because that
	// asymmetry is the endpoint's documented contract.
	g.waitForApproval(t, approver, task.LatestRunID, 1)
	if got := approver.decide(t, task.LatestRunID, "gate", "request_changes", "add a header"); got.Generation != 2 {
		t.Errorf("request_changes reported generation %d, want 2: the revision it opened", got.Generation)
	}
	g.waitForApproval(t, approver, task.LatestRunID, 2)
	approved := approver.decide(t, task.LatestRunID, "gate", "approve", "")
	if approved.Generation != 2 {
		t.Errorf("approve reported generation %d, want 2: the generation it decided", approved.Generation)
	}
	if approved.AuthorizationID == "" {
		t.Error("an approve that unblocks a publish node must report the authorization it minted")
	}

	run := g.waitForRun(t, approver, task.LatestRunID, "succeeded", 90*time.Second)

	// The run pinned the real repository's head, and published on top of it.
	if run.BaseSHA != g.baseSHA {
		t.Errorf("run base_sha = %s, want the bare repository's head %s", run.BaseSHA, g.baseSHA)
	}

	// Artifacts: two generations of patch/verdict, one publication.
	byType := map[string]int{}
	for _, a := range run.Artifacts {
		byType[a.Type]++
	}
	for _, want := range []struct {
		typ string
		n   int
	}{{"plan", 1}, {"patch", 3}, {"review_verdict", 2}, {"gate_note", 1}, {"publication", 1}} {
		if byType[want.typ] != want.n {
			t.Errorf("%s artifacts = %d, want %d (all: %v)", want.typ, byType[want.typ], want.n, byType)
		}
	}
	// patch = 2 produced + 1 seeded carry-forward; the seed is what makes
	// the second round a REVISION rather than a restart. The plan node is
	// outside the revision slice, which is why plan stays at 1.

	// Task 11 carry-forward: the run projection over ORCHESTRATOR-PRODUCED
	// rows. Every gate test in internal/runs/api stages its rows with SQL, so
	// this is the only place that proves the orchestrator materializes the
	// shape that surface reads.
	if run.Generation != 2 {
		t.Errorf("run generation = %d, want 2", run.Generation)
	}
	byGen := map[int32]*runsapi.GateDecisionSummary{}
	for _, p := range run.Phases {
		if p.NodeID == "gate" && p.Decision != nil {
			byGen[p.Generation] = p.Decision
		}
	}
	if len(byGen) != 2 {
		t.Fatalf("decided gate phases by generation = %v, want one per generation", byGen)
	}
	if byGen[1].Decision != "request_changes" || byGen[2].Decision != "approve" {
		t.Errorf("decisions by generation = %q / %q, want request_changes / approve",
			byGen[1].Decision, byGen[2].Decision)
	}
	// The line that kills the node_id-join mutant outright: a join on node_id
	// attaches both decisions to both rows and still yields two decided gates.
	if byGen[1].ExaminedArtifactID == byGen[2].ExaminedArtifactID {
		t.Error("the two rounds examined the same artifact; the revision did not regenerate")
	}
	if byGen[2].ActedByEmail != "approver@example.com" || byGen[2].SelfApproval {
		t.Errorf("approve decision attribution = %+v", byGen[2])
	}

	// Events. This is a SUBSEQUENCE, not the whole stream: the run also emits
	// phase.started/phase.succeeded per phase. What it pins is the ORDER of the
	// milestones — gate before revision, revision before the second gate,
	// intent before push before pull request, all before the run succeeds.
	g.assertEventSubsequence(t, approver, task.LatestRunID, []string{
		"run.created", "run.started",
		"phase.materialized", // plan
		"gate.waiting", "gate.decided", "run.revision_opened",
		"gate.waiting", "gate.decided",
		"publish.intent_created", "publish.pushed", "publish.pr_opened",
		"run.succeeded",
	})
	// Two decisions, TWO gate.decided events, two audit rows. One decision is
	// one event, and an approve that authorizes a publish says so on that same
	// event through authorization_id/authorizes.
	//
	// It used to be three: the approve emitted a second gate.decided carrying
	// the authorization and no decision field. The whole-branch review settled
	// it — a kind that means two things makes every consumer double-count
	// exactly the approvals that authorize a publish, and the audit's one row
	// per decision was already the honest shape. This count is now an
	// endorsement rather than a record, and it is the assertion that stops the
	// second emission coming back.
	if n := g.countEventKind(t, approver, run.ID, "gate.decided"); n != 2 {
		t.Errorf("gate.decided events = %d, want 2 (one per decision)", n)
	}

	// Audit, scoped to THIS run's own subjects. An action-only count passes on
	// any entry of that action in the database, which is what the Plan 05
	// carry-forward is about — the isolation that makes it true today is a
	// property of the fixture, not of the assertion.
	for _, sub := range []struct{ action, id string }{
		{"task.created", task.ID},
		{"run.created", run.ID},
		{"run.finished", run.ID},
	} {
		if n := g.countAuditSubject(t, sub.action, sub.id); n != 1 {
			t.Errorf("%s audit entries for subject %s = %d, want 1", sub.action, sub.id, n)
		}
	}
	// gate.decided's subject is the DECISION, not the run, so scoping to this
	// run goes through gate_decisions.run_id. Two decisions, one per generation.
	if n := g.countAuditGateDecisions(t, run.ID); n != 2 {
		t.Errorf("gate.decided audit entries for this run = %d, want 2", n)
	}

	// The remote really carries the publication.
	if len(run.Publications) != 1 {
		t.Fatalf("publications = %+v, want exactly one", run.Publications)
	}
	pub := run.Publications[0]
	if pub.Status != "published" {
		t.Errorf("publication status = %q, want published (%+v)", pub.Status, pub)
	}
	if got := g.remoteHead(t, pub.Branch); got != pub.HeadSHA {
		t.Errorf("remote %s = %q, want %q", pub.Branch, got, pub.HeadSHA)
	}
	if got := g.remoteFileAt(t, pub.HeadSHA, "CAESAR-"+goldenImplNode+".md"); got == "" {
		t.Error("the published commit does not contain the patch's file")
	}
	if got := g.remoteBranchCount(t, "caesar/"); got != 1 {
		t.Errorf("caesar branches on the remote = %d, want 1", got)
	}
	if got := g.forge.OpenPRCount(); got != 1 {
		t.Errorf("pull requests opened = %d, want 1", got)
	}
}

func TestGoldenSet_CrashInjectionAroundEveryPublishStep(t *testing.T) {
	// spec §7: "crash injection around every publish step (post-push,
	// post-PR-create, pre-DB-commit)". Each of these five windows is already
	// proved at the service level in internal/runs/publish_integration_test.go
	// (:552, :626, :821, :848, :1063). What this table adds — and the only
	// reason it is worth a whole API stack per case — is that the HUMAN-FACING
	// PROJECTION survives the crash: one publication, one pull request, one
	// branch, no leftover attention item, through the real routes.
	cases := []struct {
		name string
		arm  func(*goldenEnv)
		// disarm is per-case because the three mechanisms differ: the two fake
		// crash switches are STICKY and must be turned off, while a one-shot
		// statement fault has already disarmed itself by firing. One
		// zero-argument disarm cannot know which of the three was armed.
		disarm func(*goldenEnv)
		// fired reports that the injection triggered. For the two sticky
		// switches it can only observe the EFFECT — both fakes increment their
		// counter before they read the crash flag (fake/publisher.go:142-147,
		// fake/forge.go:159) — so it is a proxy there, not a discriminator.
		// wantAdopted is what discriminates; see below.
		fired func(*goldenEnv) bool
		// wantAdopted is the publish.adopted count, and it is the ONLY thing in
		// this table that separates "crashed, then recovered" from "never
		// crashed at all". Everything else here — one push, one branch, one
		// pull request, one intent, one consumed authorization, one pr_number
		// event — is equally true of a run that had no crash injected, which is
		// how two of these cases once passed with both fakes' crash switches
		// turned into no-ops.
		//
		// publish.adopted is emitted only on the recovery path: by
		// pushPublication when the push finds the branch already there
		// (publish.go:724-726), and by openPublication when the pull request is
		// already open (publish.go:915-917). Exactly one such event is the
		// signature of one crash recovered. It is 0 for the intent case alone,
		// where the transaction rolled back before anything reached the forge,
		// so there is nothing to adopt.
		wantAdopted int
	}{
		{
			"post-push",
			func(g *goldenEnv) { g.publisher.CrashAfterPush(true) },
			func(g *goldenEnv) { g.publisher.CrashAfterPush(false) },
			func(g *goldenEnv) bool { return g.publisher.PushCount() >= 1 }, 1,
		},
		{
			"post-pr-create",
			func(g *goldenEnv) { g.forge.CrashAfterPR(true) },
			func(g *goldenEnv) { g.forge.CrashAfterPR(false) },
			func(g *goldenEnv) bool { return g.forge.CreateAttemptCount() >= 1 }, 1,
		},
		{
			"pre-db-commit-on-push-receipt",
			func(g *goldenEnv) { g.faults.Fail("RecordPushReceipt", 0, 1) }, func(*goldenEnv) {},
			func(g *goldenEnv) bool { return g.faults.Fired("RecordPushReceipt") > 0 }, 1,
		},
		{
			"pre-db-commit-on-pr-receipt",
			func(g *goldenEnv) { g.faults.Fail("RecordPRReceipt", 0, 1) }, func(*goldenEnv) {},
			func(g *goldenEnv) bool { return g.faults.Fired("RecordPRReceipt") > 0 }, 1,
		},
		{
			"pre-db-commit-on-intent",
			func(g *goldenEnv) { g.faults.Fail("CreatePublishIntent", 0, 1) }, func(*goldenEnv) {},
			func(g *goldenEnv) bool { return g.faults.Fired("CreatePublishIntent") > 0 }, 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newGoldenEnv(t)
			requester := goldenLogin(t, g.srv, "requester@example.com", goldenPassword)
			approver := goldenLogin(t, g.srv, "approver@example.com", goldenPassword)
			// publish_resumable parks 5-15s per retry; that is real time the
			// test refuses to spend.
			startBackdatingParked(t, g.pool)

			task := g.createTask(t, requester, "Add a CHANGELOG")
			g.waitForApproval(t, approver, task.LatestRunID, 1)

			c.arm(g)
			approver.decide(t, task.LatestRunID, "gate", "approve", "")
			g.waitUntil(t, "the armed failure for "+c.name+" firing", 60*time.Second, func() bool { return c.fired(g) })

			// Cross a second boundary before disarming. The commit stamp gitops
			// writes is RFC3339, second resolution: a retry inside the same
			// second rebuilds the same sha from the wall clock too, so without
			// this the adopt path would pass with the date unpinned.
			time.Sleep(1100 * time.Millisecond)
			c.disarm(g)

			run := g.waitForRun(t, approver, task.LatestRunID, "succeeded", 90*time.Second)

			// Branch CREATIONS, not pushes. PushCount is not the property and is
			// not stable: CrashAfterPush is sticky, so it keeps
			// pushing-and-crashing until disarmed (measured 8), and a receipt
			// failure costs the push plus its recovery (measured 2). One branch
			// creation is the invariant — the retry adopted rather than replaced.
			if got := g.publisher.CreatedCount(); got != 1 {
				t.Errorf("branch creations = %d, want 1: the retry must ADOPT, not replace", got)
			}
			if got := g.forge.OpenPRCount(); got != 1 {
				t.Errorf("pull requests opened = %d, want 1", got)
			}
			if n := g.countEventKind(t, approver, run.ID, "publish.adopted"); n != c.wantAdopted {
				t.Errorf("publish.adopted events = %d, want %d: without this the case passes "+
					"with its crash injection disabled — recovery is what it exists to prove", n, c.wantAdopted)
			}
			if got := g.remoteBranchCount(t, "caesar/"); got != 1 {
				t.Errorf("caesar branches on the remote = %d, want 1", got)
			}
			if got := g.countIntents(t, run.ID); got != 1 {
				t.Errorf("publish intents = %d, want 1", got)
			}
			if got := g.countAuthorizations(t, run.ID, "consumed"); got != 1 {
				t.Errorf("consumed authorizations = %d, want 1: the token is single-use through a crash too", got)
			}
			if len(run.Publications) != 1 || run.Publications[0].Status != "published" {
				t.Errorf("publications = %+v", run.Publications)
			}
			if n := g.countEventsCarrying(t, approver, run.ID, "pr_number"); n != 1 {
				t.Errorf("events announcing a pull request = %d, want 1: a crash must not double the projection", n)
			}
			if n := g.countEventKind(t, approver, run.ID, "publish.intent_created"); n != 1 {
				t.Errorf("publish.intent_created events = %d, want 1: a crash must not open a second publication", n)
			}
			if n := g.countApprovals(t, approver, run.ID); n != 0 {
				t.Errorf("the run still shows %d attention items after succeeding", n)
			}
		})
	}
}

func TestGoldenSet_CrashBeforeTheIntentCommitsPublishesNothing(t *testing.T) {
	// The one case where "resume" must mean "start over": if the intent's
	// transaction never committed, no branch exists and the authorization was
	// never spent. The consume and the intent insert are ONE transaction, and
	// that is what this asserts.
	//
	// The fault fails CreatePublishIntent until explicitly disarmed rather than
	// once, because a one-shot is disarmed by firing and publish_not_started
	// requeues on a {0,0,0} schedule (retry.go:27) — the retry has re-consumed
	// the approval before any reader can see the window. Held open, the window
	// is stable: no reader on another connection can ever see the rolled-back
	// transaction's work.
	g := newGoldenEnv(t)
	requester := goldenLogin(t, g.srv, "requester@example.com", goldenPassword)
	approver := goldenLogin(t, g.srv, "approver@example.com", goldenPassword)
	task := g.createTask(t, requester, "Add a CHANGELOG")
	g.waitForApproval(t, approver, task.LatestRunID, 1)

	g.faults.FailUntilDisarmed("CreatePublishIntent")
	approver.decide(t, task.LatestRunID, "gate", "approve", "")
	g.waitUntil(t, "the armed CreatePublishIntent failure firing", 60*time.Second,
		func() bool { return g.faults.Fired("CreatePublishIntent") > 0 })

	// While it is still armed, nothing the failed transaction did is visible.
	if got := g.countAuthorizations(t, task.LatestRunID, "valid"); got != 1 {
		t.Fatalf("valid authorizations = %d, want 1: consumption and intent creation are atomic", got)
	}
	if got := g.countIntents(t, task.LatestRunID); got != 0 {
		t.Fatalf("publish intents = %d, want 0: the transaction that would have created one never committed", got)
	}
	if got := g.publisher.PushCount(); got != 0 {
		t.Fatalf("pushes = %d, want 0: nothing may reach the remote before the intent commits", got)
	}

	g.faults.Disarm("CreatePublishIntent")
	run := g.waitForRun(t, approver, task.LatestRunID, "succeeded", 90*time.Second)

	// And then exactly one of everything: the retry started over rather than
	// resuming half a publication.
	if got := g.countIntents(t, run.ID); got != 1 {
		t.Errorf("publish intents = %d, want 1", got)
	}
	if got := g.countAuthorizations(t, run.ID, "consumed"); got != 1 {
		t.Errorf("consumed authorizations = %d, want 1", got)
	}
	if got := g.countAuthorizations(t, run.ID, "valid"); got != 0 {
		t.Errorf("valid authorizations = %d, want 0: the approval is spent", got)
	}
	if got := g.publisher.CreatedCount(); got != 1 {
		t.Errorf("branch creations = %d, want 1: one approval, one branch", got)
	}
	if got := g.remoteBranchCount(t, "caesar/"); got != 1 {
		t.Errorf("caesar branches on the remote = %d, want 1", got)
	}
	if len(run.Publications) != 1 || run.Publications[0].Status != "published" {
		t.Errorf("publications = %+v", run.Publications)
	}
}

func TestGoldenSet_RejectedGateLeavesTheRemoteUntouched(t *testing.T) {
	g := newGoldenEnv(t)
	requester := goldenLogin(t, g.srv, "requester@example.com", goldenPassword)
	approver := goldenLogin(t, g.srv, "approver@example.com", goldenPassword)
	task := g.createTask(t, requester, "Add a CHANGELOG")
	g.waitForApproval(t, approver, task.LatestRunID, 1)
	approver.decide(t, task.LatestRunID, "gate", "reject", "wrong approach")

	run := g.waitForRun(t, approver, task.LatestRunID, "failed", 30*time.Second)
	if run.Outcome == nil || run.Outcome.Code != "gate_rejected" {
		t.Fatalf("outcome = %+v", run.Outcome)
	}
	if got := g.remoteBranchCount(t, "caesar/"); got != 0 {
		t.Errorf("a rejected gate published %d branches", got)
	}
	if g.publisher.PushCount() != 0 || g.forge.OpenPRCount() != 0 {
		t.Errorf("a rejected gate must reach no forge at all: pushes = %d, pull requests = %d",
			g.publisher.PushCount(), g.forge.OpenPRCount())
	}
	if got := g.countAuthorizations(t, run.ID, "valid"); got != 0 {
		t.Errorf("valid authorizations = %d, want 0: a rejection authorizes nothing", got)
	}
}
