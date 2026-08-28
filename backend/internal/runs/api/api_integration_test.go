//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
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

// patience bounds a request that could stream. See get.
const patience = 5 * time.Second

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
	for _, v := range p.h.versions {
		if v.workflowID == workflowID && v.number == version {
			return p.pinned(v), nil
		}
	}
	return runs.PinnedDefinition{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
}

func (p spinePorts) DefinitionByVersionID(_ context.Context, versionID uuid.UUID) (runs.PinnedDefinition, error) {
	for _, v := range p.h.versions {
		if v.id == versionID {
			return p.pinned(v), nil
		}
	}
	return runs.PinnedDefinition{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
}

func (p spinePorts) pinned(v stagedVersion) runs.PinnedDefinition {
	return runs.PinnedDefinition{
		VersionID: v.id, WorkflowID: v.workflowID, Version: v.number,
		ContentHash: v.contentHash, Doc: v.doc,
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

// ProjectNames, RepoSlugs and WorkflowNames are runs.RunLabelSource, served
// from the same harness rows every other port here answers from. They are real
// lookups rather than constants because the list endpoint's labels are exactly
// what a caller cannot get anywhere else, and a double that invented them would
// make "the row carries the labels a card renders" true of nothing.
//
// A missing id is an ABSENT entry and never an error — the port's contract, and
// the reason a deleted repository does not take out the list of runs that name
// it.
// labelsUnavailable is the fault a real adapter reports when the store behind
// it is unreachable — distinct from a missing id, which is an absent entry.
func (p spinePorts) labelsUnavailable() error {
	if p.h.labelsDown {
		return xerrors.E(xerrors.ClassInternal, "label store unreachable")
	}
	return nil
}

func (p spinePorts) ProjectNames(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	if err := p.labelsUnavailable(); err != nil {
		return nil, err
	}
	names := map[uuid.UUID]string{p.h.projectID: "spine", p.h.projectB: "spine-b"}
	out := map[uuid.UUID]string{}
	for _, id := range ids {
		if n, ok := names[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func (p spinePorts) RepoSlugs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	if err := p.labelsUnavailable(); err != nil {
		return nil, err
	}
	out := map[uuid.UUID]string{}
	for _, id := range ids {
		r, err := p.Repo(ctx, id)
		if err != nil {
			continue
		}
		out[id] = r.Slug
	}
	return out, nil
}

func (p spinePorts) WorkflowNames(_ context.Context, versionIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	if err := p.labelsUnavailable(); err != nil {
		return nil, err
	}
	out := map[uuid.UUID]string{}
	for _, vid := range versionIDs {
		for _, v := range p.h.versions {
			if v.id == vid {
				out[vid] = p.h.workflowNames[v.workflowID]
			}
		}
	}
	return out, nil
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

// stagedVersion is one workflow version the definition port answers for. The
// document COLUMN is never read — the port serves doc, and the row exists only
// because tasks.workflow_version_id is a foreign key — so the two are kept
// together here rather than left to be matched up by id at two call sites.
type stagedVersion struct {
	id uuid.UUID
	// workflowID is the workflow this version is a version OF. It was implicit
	// — one workflow per harness — until the run list gained a workflow_id
	// filter, which cannot be falsified against a fixture with one workflow in
	// it. Carrying it per version also means `version` is no longer a unique
	// key over this slice: two workflows both have a version 1.
	workflowID  uuid.UUID
	number      int32
	contentHash string
	doc         runs.DefinitionDoc
}

// The workflow versions this harness registers. Version 1 is gatedDoc, which
// every test that does not ask for another graph runs on; the other two exist
// because a gate's EVIDENCE is a property of the graph's shape, and the two
// shapes gatedDoc cannot express are "the gate examines a plan" and "a review
// verdict feeds the gate".
const (
	gatedVersion       int32 = 1
	planGateVersion    int32 = 2
	verdictGateVersion int32 = 3
)

// workflowName is the workflow every staged version belongs to unless a test
// asks for the other one. They are constants because the label port answers
// with them and the list assertions read them back: a literal in two places is
// a test that agrees with itself by coincidence.
//
// workflowNameB is the SECOND workflow, and it exists for exactly one reason:
// `workflow_id=X` cannot be told apart from no filter at all while every run in
// the fixture belongs to one workflow. It stages a single version, because the
// filter's other property — that a task pinned to version 2 of a workflow still
// matches that workflow — is proved on the FIRST workflow, which already has
// three.
const (
	workflowName  = "spine-wf"
	workflowNameB = "spine-wf-b"
)

// planGateDoc is a gate over a PLAN, with a review node hanging off the same
// plan. The review finishes later, so "the newest artifact on the run" and "the
// artifact this gate examines" are different rows — which is the only shape in
// which a projection that guessed at the examined artifact can be caught.
func planGateDoc() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "review", Kind: runs.KindReview, Harness: "cc", Instructions: "Review the plan."},
			{ID: "gate", Kind: runs.KindGate},
		},
		Edges: []runs.DefEdge{
			{From: "plan", To: "review", Artifact: runs.ArtifactKindPlan},
			{From: "plan", To: "gate", Artifact: runs.ArtifactKindPlan},
			{From: "review", To: "gate", Artifact: runs.ArtifactKindReviewVerdict},
		},
	}
}

// verdictGateDoc is a gate that consumes a review verdict as well as the patch
// it examines, and it carries a SECOND review node whose verdict the gate does
// NOT consume.
//
// That second node is the whole point. With one verdict producer, "the verdict
// the compiled plan names" and "the newest review_verdict on the run" are the
// same row, and a projection that resolved the summary the second way would
// pass every assertion. It is a legal graph — nothing requires a review to feed
// a gate — and it is what a workflow with an advisory second reviewer looks
// like.
func verdictGateDoc() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement it."},
			{ID: "review", Kind: runs.KindReview, Harness: "cc", Instructions: "Review the patch."},
			{ID: "late", Kind: runs.KindReview, Harness: "cc", Instructions: "Review it again."},
			{ID: "gate", Kind: runs.KindGate, Revision: &runs.DefRevision{Target: "impl", MaxRounds: 2}},
		},
		Edges: []runs.DefEdge{
			{From: "impl", To: "review", Artifact: runs.ArtifactKindPatch},
			{From: "impl", To: "late", Artifact: runs.ArtifactKindPatch},
			{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch},
			{From: "review", To: "gate", Artifact: runs.ArtifactKindReviewVerdict},
		},
	}
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
	// workflowB is the second workflow. Only the run list's workflow_id filter
	// stages runs on it; every other test in this package runs on workflowID.
	workflowB uuid.UUID
	// workflowNames is what the label port answers, keyed by workflow id. A map
	// rather than a constant now that a version does not imply which workflow
	// named it.
	workflowNames map[uuid.UUID]string
	// versionID is version 1 — gatedDoc, what every test that does not ask for
	// another graph runs on.
	versionID uuid.UUID
	// versions is every workflow version the definition port can answer for.
	// One doc per harness was enough until a gate's evidence had to be read off
	// graphs that differ in SHAPE — a gate over a plan, a gate with a review
	// verdict feeding it, a gate with none — and a fixture cannot stage those
	// against a single pinned definition.
	versions  []stagedVersion
	admin     authn.Principal
	runner    authn.Principal
	approver  authn.Principal
	approverB authn.Principal
	viewer    authn.Principal
	outsider  authn.Principal
	// labelsDown makes every label lookup fail, which is the only way to reach
	// the one branch in this package that swallows an error instead of
	// reporting it (handleRerunTask). It is a field rather than a separate
	// double because the port is answered by spinePorts, which holds *harness.
	labelsDown bool
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

// fakeStreamServer answers the outbound stream port. runs must never import
// realtime (bounded contexts are peers), so this double writes SSE bytes of
// its own rather than borrowing the real codec — which also keeps these tests
// about NEGOTIATION and nothing else.
type fakeStreamServer struct {
	mu    sync.Mutex
	calls []streamCall
	// err is what the port reports. The port's contract is that an error means
	// NOTHING was written yet, so this double writes nothing when err is set.
	err error
}

type streamCall struct {
	RunID       uuid.UUID
	ProjectID   uuid.UUID
	UserID      uuid.UUID
	LastEventID int64
	HasLastID   bool
}

func (f *fakeStreamServer) ServeRunStream(
	_ context.Context, w http.ResponseWriter, runID, projectID, userID uuid.UUID, lastEventID int64, hasLastID bool,
) error {
	f.mu.Lock()
	f.calls = append(f.calls, streamCall{
		RunID: runID, ProjectID: projectID, UserID: userID,
		LastEventID: lastEventID, HasLastID: hasLastID,
	})
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "id: 1\nevent: run.created\ndata: {\"id\":1}\n\n")
	return nil
}

func (f *fakeStreamServer) served() []streamCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]streamCall(nil), f.calls...)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWithStreams(t, &fakeStreamServer{})
}

// newHarnessWithStreams is newHarness with the outbound stream port supplied:
// the negotiation tests need to see what the port was handed, and one of them
// needs the port MISSING.
func newHarnessWithStreams(t *testing.T, streams runsapi.StreamServer) *harness {
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
		`INSERT INTO workflows (name) VALUES ($1) RETURNING id`, workflowName).Scan(&h.workflowID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO workflows (name) VALUES ($1) RETURNING id`, workflowNameB).Scan(&h.workflowB); err != nil {
		t.Fatal(err)
	}
	h.workflowNames = map[uuid.UUID]string{h.workflowID: workflowName, h.workflowB: workflowNameB}
	h.versionID = h.registerVersion(t, h.workflowID, gatedVersion, "deadbeef", gatedDoc())
	h.registerVersion(t, h.workflowID, planGateVersion, "deadbeef02", planGateDoc())
	h.registerVersion(t, h.workflowID, verdictGateVersion, "deadbeef03", verdictGateDoc())
	// Workflow B stages gatedDoc again, deliberately: the filter is about which
	// workflow a task points at, not about the graph, and a second shape here
	// would make a failure ambiguous between the two.
	h.registerVersion(t, h.workflowB, gatedVersion, "deadbeefb1", gatedDoc())

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
		fake.NewPublisher(nil), ports, runs.Params{Snapshot: runs.DefaultSnapshot(), Labels: ports})

	mux := http.NewServeMux()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	httpx.Mount(mux, runsapi.Operations(runsapi.New(svc, h.ports, h.ports, h.ports, streams, log)))
	h.srv = httptest.NewServer(httpx.Chain(mux, httpx.WithCorrelationID, httpx.AccessLog(log), httpx.CheckOrigin("http://caesar.test")))
	t.Cleanup(h.srv.Close)
	return h
}

// registerVersion inserts a workflow version row and teaches the definition
// port the document it pins. content_hash is a parameter because the schema
// makes it UNIQUE per workflow, so two versions cannot share one.
//
// The workflow is a parameter rather than h.workflowID because the definition
// port now resolves on the PAIR: two workflows each have a version 1, and a
// lookup by number alone would answer for whichever was staged first.
func (h *harness) registerVersion(t *testing.T, workflowID uuid.UUID, number int32, contentHash string, doc runs.DefinitionDoc) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := h.pool.QueryRow(context.Background(),
		`INSERT INTO workflow_versions (workflow_id, version, content_hash, document)
		 VALUES ($1, $2, $3, '{"schema":1,"nodes":[]}'::jsonb) RETURNING id`,
		workflowID, number, contentHash,
	).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h.versions = append(h.versions, stagedVersion{
		id: id, workflowID: workflowID, number: number, contentHash: contentHash, doc: doc,
	})
	return id
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

// get is do for a request whose HEADERS are the subject: no body, no
// Content-Type of its own, and whatever Accept or Last-Event-ID the caller
// wants tested.
//
// It carries a deadline and do does not, and the asymmetry is the whole reason
// it is written out here. This is the only helper that can reach the streaming
// branch, and a real stream handler RETURNS ONLY WHEN THE CLIENT GOES AWAY.
// Nothing hangs in THIS suite, because fakeStreamServer answers and returns —
// but the port now carries the live Streamer in internal/app, so the shape
// under test is one that does not return, and a negotiation test without a
// deadline is one substituted double away from a suite timeout with no name on
// it. The deadline turns "it never answered" into a named failure at patience.
// The neighbouring realtime/api package has the same helper for the same
// reason.
func (h *harness) get(t *testing.T, path, token string, headers http.Header) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), patience)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Cookie", authn.SessionCookie+"="+token)
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
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
	return h.newTaskOnVersion(repoID, title, gatedVersion)
}

func (h *harness) newTaskOnVersion(repoID uuid.UUID, title string, version int32) runsapi.CreateTaskRequest {
	return h.newTaskOnWorkflowVersion(repoID, title, h.workflowID, version)
}

func (h *harness) newTaskOnWorkflowVersion(repoID uuid.UUID, title string, workflowID uuid.UUID, version int32) runsapi.CreateTaskRequest {
	return runsapi.CreateTaskRequest{
		RepoID: repoID.String(), WorkflowID: workflowID.String(), WorkflowVersion: version,
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
	return h.createTaskOnVersion(t, token, repoID, title, gatedVersion)
}

func (h *harness) createTaskOnVersion(t *testing.T, token string, repoID uuid.UUID, title string, version int32) runsapi.TaskResponse {
	t.Helper()
	return h.createTaskOnWorkflowVersion(t, token, repoID, title, h.workflowID, version)
}

func (h *harness) createTaskOnWorkflowVersion(t *testing.T, token string, repoID uuid.UUID, title string, workflowID uuid.UUID, version int32) runsapi.TaskResponse {
	t.Helper()
	resp := h.do(t, http.MethodPost, "/api/v1/tasks", token, h.newTaskOnWorkflowVersion(repoID, title, workflowID, version))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create task = %d, want 201", resp.StatusCode)
	}
	return decodeInto[runsapi.TaskResponse](t, resp)
}

// stagedTreeHash is the git tree hash the staged patch claims. Any non-empty
// value satisfies mintAuthorization; nothing in this suite publishes.
// supersededTreeHash belongs to the earlier round's patch and exists only to be
// a DIFFERENT value: an approval that bound it would be approving content the
// revision replaced.
const (
	stagedTreeHash     = "1111111111111111111111111111111111111111"
	supersededTreeHash = "2222222222222222222222222222222222222222"
	// liveGateAttempt is the attempt ordinal stageGate gives the WAITING gate.
	// It is deliberately not equal to that gate's generation — see stageGate.
	liveGateAttempt = 3
)

// gateStamps is what stageGate wrote and what the projections have to carry
// back unchanged. Every value is read off the writing statement's RETURNING
// rather than computed in Go: the timestamps come from the DATABASE clock,
// while this harness injects an app clock pinned to a fixed date in 2026, so a
// Go-side time.Now() would compare two unrelated clocks.
type gateStamps struct {
	waitingSince time.Time // the LIVE gate attempt's created_at
	priorActedAt time.Time // the SUPERSEDED attempt's decision's acted_at
	priorPatchID string    // the artifact that superseded decision examined
	livePatchID  string    // the artifact a decision on the live gate examines
	wallDeadline time.Time // the run's deadline, as this fixture rewrote it
}

// parkAtGate puts a run in the state a materialized, undecided gate leaves it
// in and hands back its base SHA.
//
// The wall deadline is rewritten off the DATABASE clock deliberately. Every
// deciding predicate reads now() in the statement that mutates, while this
// harness's app clock is pinned to a fixed date in 2026 — so a run left with
// the deadline intake computed would be refused as expired on any day after it,
// and every assertion below would be about the wrong refusal.
//
// Every timestamp a fixture writes afterwards is a distinct offset from that
// same clock. Equal timestamps would let a projection read the wrong column and
// still match.
func (h *harness) parkAtGate(t *testing.T, runID uuid.UUID, generation int32) (string, time.Time) {
	t.Helper()
	var baseSHA string
	var deadline time.Time
	if err := h.pool.QueryRow(context.Background(),
		`UPDATE runs SET status = 'waiting', generation = $2, started_at = now() - interval '90 minutes',
		        wall_deadline = now() + interval '1 hour'
		 WHERE id = $1 RETURNING base_sha, wall_deadline`,
		runID, generation).Scan(&baseSHA, &deadline); err != nil {
		t.Fatal(err)
	}
	return baseSHA, deadline
}

// stageArtifact writes one artifact row. Ages are passed as whole minutes and
// turned into intervals in SQL, so every row's timestamp comes off the same
// database clock as the run's deadline and no two of them collide.
func (h *harness) stageArtifact(t *testing.T, runID uuid.UUID, node, artifactType string,
	attempt, generation, agoMin int32, payload string,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := h.pool.QueryRow(context.Background(),
		`INSERT INTO artifacts (id, run_id, node_id, attempt, generation, type, payload, parent_ids, created_at)
		 VALUES (uuidv7(), $1, $2, $3, $4, $5, $6::jsonb, '[]'::jsonb,
		         now() - make_interval(mins => $7::int))
		 RETURNING id`,
		runID, node, attempt, generation, artifactType, payload, agoMin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// stagePhase writes one phase_runs row. finishedMin is a *int32 rather than a
// bool: make_interval of NULL is NULL, so the same expression writes the
// timestamp or leaves the column unset.
func (h *harness) stagePhase(t *testing.T, runID uuid.UUID, node, status string,
	attempt, generation, agoMin int32, finishedMin *int32,
) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := h.pool.QueryRow(context.Background(),
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, status, generation, finished_at, created_at)
		 VALUES ($1, $2, $4, 3, $3, $5,
		         now() - make_interval(mins => $6::int),
		         now() - make_interval(mins => $7::int))
		 RETURNING id`,
		runID, node, status, attempt, generation, finishedMin, agoMin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// mins boxes a whole-minute age for stagePhase's optional finished_at.
func mins(m int32) *int32 { return &m }

// patchPayload is what a patch artifact carries: mintAuthorization reads both
// fields off it, and an approval over a patch that has neither is refused.
func patchPayload(treeHash, baseSHA string) string {
	return fmt.Sprintf(`{"tree_hash":%q,"base_sha":%q}`, treeHash, baseSHA)
}

// stageGate parks a run at a gate awaiting a decision, without a conveyor.
//
// This suite answers HTTP and authorization questions — statuses, grants,
// scoping, body validation — and none of them need an agent to have run, so the
// rows a materialized gate leaves behind are written directly. The conveyor
// that produces them for real is Task 13's golden set.
//
// It stages TWO rounds, not one, and that is load-bearing rather than colour:
//
//	generation 1: impl produced a patch, the gate was sent back for changes
//	              — both rows superseded, the decision row still there
//	generation 2: impl re-ran, and its gate is the one now waiting
//
// One round cannot falsify the projection's central claim. After a revision
// both gate attempts carry the SAME node id and differ only by attempt row, so
// a join keyed on node_id instead of phase_run_id smears the newer verdict
// across both — and a fixture with a single gate attempt has no second row for
// it to smear onto. Two rounds also make attempt and generation something other
// than the constant 1, which is what an inbox entry's fields are read against.
//
// What it deliberately does NOT stage is the pair of seed artifacts a real
// revision writes (the gate note and the carried-forward patch, both at attempt
// 0). Nothing on this surface reads them, and they would only pad the artifact
// count this file asserts on.
func (h *harness) stageGate(t *testing.T, task runsapi.TaskResponse) (uuid.UUID, gateStamps) {
	t.Helper()
	ctx := context.Background()
	runID, err := uuid.Parse(task.LatestRunID)
	if err != nil {
		t.Fatal(err)
	}
	var st gateStamps
	baseSHA, wallDeadline := h.parkAtGate(t, runID, 2)
	st.wallDeadline = wallDeadline

	patch := func(attempt, generation, agoMin int32, hash string) uuid.UUID {
		t.Helper()
		return h.stageArtifact(t, runID, "impl", "patch", attempt, generation, agoMin,
			patchPayload(hash, baseSHA))
	}
	phase := func(node, status string, attempt, generation, agoMin int32, finishedMin *int32) uuid.UUID {
		t.Helper()
		return h.stagePhase(t, runID, node, status, attempt, generation, agoMin, finishedMin)
	}
	at := mins

	// Generation 1: the implementation failed once, retried, produced a patch,
	// and the gate sent the round back for changes.
	//
	// The failed retry is not colour either. A revision bumps attempt and
	// generation together, so without one extra attempt somewhere every row in
	// this fixture would carry attempt == generation — and a projection that
	// swapped the two columns would satisfy every assertion in this file.
	phase("impl", "failed", 1, 1, 70, at(65))
	priorPatch := patch(2, 1, 60, supersededTreeHash)
	st.priorPatchID = priorPatch.String()
	phase("impl", "superseded", 2, 1, 60, at(50))
	priorGate := phase("gate", "superseded", 1, 1, 50, at(45))
	// The requester decides their own gate here, so the superseded round differs
	// from the live one in every projected field — verdict, decider, the
	// self-approval marker and the artifact examined — and not only in verdict.
	// requester_id is read off the row rather than passed in, so this stays
	// correct for a run staged in either project.
	if err := h.pool.QueryRow(ctx,
		`INSERT INTO gate_decisions (phase_run_id, run_id, node_id, generation, decision,
		                             examined_artifact_id, note, acted_by, self_approval, acted_at)
		 SELECT $1, $2, 'gate', 1, 'request_changes', $3, 'the first round needed work',
		        t.requester_id, true, now() - interval '45 minutes'
		 FROM runs r JOIN tasks t ON t.id = r.task_id WHERE r.id = $2
		 RETURNING acted_at`,
		priorGate, runID, priorPatch).Scan(&st.priorActedAt); err != nil {
		t.Fatal(err)
	}

	// Generation 2: re-run, and waiting on its own gate. The attempt ordinal
	// continues rather than restarting — see MaxAttemptForNode.
	st.livePatchID = patch(3, 2, 20, stagedTreeHash).String()
	phase("impl", "succeeded", 3, 2, 20, at(15))
	// The live gate is staged at attempt 3, generation 2, and the gap is
	// deliberate.
	//
	// A gate is materialized once per generation from generation 1, so in every
	// run this system can actually produce a gate's attempt EQUALS its
	// generation — which means the inbox DTO could transpose the two columns
	// and nothing would ever see it. The transposition is the whole risk this
	// row exists to catch, so the fixture forces the divergence rather than
	// modelling a run, exactly as the service-level guard does when it forces
	// generation 3 against attempt 1 (gates_integration_test.go). This file
	// stages rows directly for precisely this kind of question; the conveyor
	// that produces them for real is the golden set.
	liveGate := phase("gate", "waiting_decision", liveGateAttempt, 2, 10, nil)
	if err := h.pool.QueryRow(ctx,
		`SELECT created_at FROM phase_runs WHERE id = $1`, liveGate).Scan(&st.waitingSince); err != nil {
		t.Fatal(err)
	}
	return runID, st
}

// gatedRun is createTask + stageGate: a run of the caller's, sitting at its gate
// with one spent revision round behind it.
func (h *harness) gatedRun(t *testing.T, token string, repoID uuid.UUID, title string) (runsapi.TaskResponse, uuid.UUID, gateStamps) {
	t.Helper()
	task := h.createTaskInRepo(t, token, repoID, title)
	runID, stamps := h.stageGate(t, task)
	return task, runID, stamps
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
		assertRunLabels(t, "GET /runs/{id}", run, "ship it")
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
		// Shallow means NO PHASES, not "no labels". Every row of this list is a
		// card too, and the labels are resolved once for the whole list because
		// every run of one task shares the task's.
		assertRunLabels(t, "GET /tasks/{id}/runs", list.Runs[0], "ship it")
	})
}

// assertRunLabels holds the three header labels of a RunResponse to their real
// values. It exists because "required in the contract" pins the SHAPE and not
// the content: RunResponse's task_title, repo_slug and workflow_name shipped
// for a round emitted by three handlers and asserted by none, and deleting the
// resolution from all three left the whole suite green. Blank is the failure
// mode — a run whose header renders nothing — so blank is what this refuses.
func assertRunLabels(t *testing.T, where string, run runsapi.RunResponse, wantTitle string) {
	t.Helper()
	if run.TaskTitle != wantTitle {
		t.Errorf("%s task_title = %q, want %q", where, run.TaskTitle, wantTitle)
	}
	if run.RepoSlug != "spine/repo" {
		t.Errorf("%s repo_slug = %q, want the staged repo's slug", where, run.RepoSlug)
	}
	if run.WorkflowName != workflowName {
		t.Errorf("%s workflow_name = %q, want %q", where, run.WorkflowName, workflowName)
	}
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
		// A re-run lands you on the run screen, so its header is rendered from
		// this very response and never from a follow-up read.
		assertRunLabels(t, "POST /tasks/{id}/runs", second, "ship it")
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

// TestRunsAPI_RerunDegradesRatherThanFailsOnALabelFault pins the one asymmetry
// in this package: rerun swallows a label fault, the read paths report it.
//
// The reason is what has already happened when the lookup runs. Rerun has
// COMMITTED a run; a 500 would tell the client its mutation failed when it
// succeeded, and the retry that follows hits the one-live-run-per-task
// constraint and surfaces as a 409 nobody can explain. A header rendering three
// blank strings costs a refresh. GET has committed nothing, so it has nothing
// to protect and reports the fault honestly.
func TestRunsAPI_RerunDegradesRatherThanFailsOnALabelFault(t *testing.T) {
	h := newHarness(t)
	task := h.createTask(t, "admin-tok", "ship it")

	h.labelsDown = true
	t.Cleanup(func() { h.labelsDown = false })

	resp := h.do(t, http.MethodPost, "/api/v1/tasks/"+task.ID+"/runs", "runner-tok", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("rerun with the label store down = %d, want 201 — the run was already written",
			resp.StatusCode)
	}
	created := decodeInto[runsapi.RunResponse](t, resp)
	if created.TaskTitle != "" || created.RepoSlug != "" || created.WorkflowName != "" {
		t.Errorf("degraded rerun invented labels: %q/%q/%q",
			created.TaskTitle, created.RepoSlug, created.WorkflowName)
	}

	// The read path, same fault, same request second: still a fault.
	if r := h.do(t, http.MethodGet, "/api/v1/runs/"+created.ID, "viewer-tok", nil); r.StatusCode != http.StatusInternalServerError {
		t.Errorf("GET /runs/{id} with the label store down = %d, want 500 — "+
			"a read has no committed write to protect and must not hide the fault", r.StatusCode)
	}

	// And the mutation really landed, which is the whole reason the 201 is
	// right: the run is readable, pending, and labelled once the store is back.
	h.labelsDown = false
	run := decodeInto[runsapi.RunResponse](t, h.do(t, http.MethodGet, "/api/v1/runs/"+created.ID, "viewer-tok", nil))
	if run.ID != created.ID || run.Status != string(runs.RunPending) {
		t.Fatalf("the degraded rerun's run reads back as %+v, want a pending run", run)
	}
	assertRunLabels(t, "GET /runs/{id} after the fault cleared", run, "ship it")
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

// The poll's wire shape is a published contract, and content negotiation is
// only allowed to ADD a branch to this endpoint. The assertion is the exact
// bytes rather than "still decodes as JSON": only field names, field order and
// the absence of new fields make a client's parser keep working, and only a
// literal pins all three.
func TestRunsAPI_ListRunEventsAcceptJSONStillPolls(t *testing.T) {
	streams := &fakeStreamServer{}
	h := newHarnessWithStreams(t, streams)
	task := h.createTask(t, "admin-tok", "poll me")

	resp := h.get(t, "/api/v1/runs/"+task.LatestRunID+"/events", "viewer-tok", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var got runsapi.RunEventsResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("body %q is not a RunEventsResponse: %v", raw, err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("events = %+v, want exactly the one run.created", got.Events)
	}
	// The id and the timestamp are the database's; the FIELD NAMES around them
	// are what this test exists to pin, so they are literals here.
	payload, err := json.Marshal(got.Events[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	occurred, err := json.Marshal(got.Events[0].OccurredAt)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"events":[{"id":%d,"kind":"run.created","payload":%s,"occurred_at":%s}]}`,
		got.Events[0].ID, payload, occurred)
	if body := strings.TrimSpace(string(raw)); body != want {
		t.Errorf("the poll response moved:\n got %s\nwant %s", body, want)
	}
	if calls := streams.served(); len(calls) != 0 {
		t.Errorf("the poll reached the stream port: %+v", calls)
	}
}

func TestRunsAPI_ListRunEventsAcceptEventStreamStreams(t *testing.T) {
	streams := &fakeStreamServer{}
	h := newHarnessWithStreams(t, streams)
	task := h.createTask(t, "admin-tok", "stream me")

	resp := h.get(t, "/api/v1/runs/"+task.LatestRunID+"/events", "viewer-tok", http.Header{
		"Accept":        {"text/event-stream"},
		"Last-Event-ID": {"12"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "id: 1\n") {
		t.Errorf("body = %q, want the stream's frames", body)
	}

	calls := streams.served()
	if len(calls) != 1 {
		t.Fatalf("stream port calls = %+v, want exactly one", calls)
	}
	wantRun, err := uuid.Parse(task.LatestRunID)
	if err != nil {
		t.Fatal(err)
	}
	// ProjectID is the run's own project, and this is the only test that sees
	// it travel the whole real path: an HTTP request, a run resolved from the
	// database, its task read for the project, and the port handed the result.
	// Everything nearer the port can be satisfied by a caller passing anything
	// at all — a wrong project would be caught here and nowhere else, and a
	// zero one would leave the stream unable to close on a partial revocation.
	want := streamCall{
		RunID: wantRun, ProjectID: h.projectID, UserID: h.viewer.UserID,
		LastEventID: 12, HasLastID: true,
	}
	if calls[0] != want {
		t.Errorf("stream port call = %+v, want %+v", calls[0], want)
	}
}

// A client that spells out that it does NOT want a stream must not be given
// one — which is the whole reason the Accept header is parsed rather than
// searched for a substring.
func TestRunsAPI_ListRunEventsQZeroTakesJSON(t *testing.T) {
	streams := &fakeStreamServer{}
	h := newHarnessWithStreams(t, streams)
	task := h.createTask(t, "admin-tok", "poll me too")

	resp := h.get(t, "/api/v1/runs/"+task.LatestRunID+"/events", "viewer-tok", http.Header{
		"Accept": {"application/json, text/event-stream;q=0"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := decodeInto[runsapi.RunEventsResponse](t, resp); len(got.Events) != 1 {
		t.Errorf("events = %+v, want the one run.created", got.Events)
	}
	if calls := streams.served(); len(calls) != 0 {
		t.Errorf("a q=0 refusal still reached the stream port: %+v", calls)
	}
}

// A missing stream port is a WIRING omission. Answering the poll instead would
// make it invisible: the endpoint would look like it works, and the streaming
// half would simply never exist.
func TestRunsAPI_ListRunEventsWithoutAStreamServerIs500(t *testing.T) {
	h := newHarnessWithStreams(t, nil)
	task := h.createTask(t, "admin-tok", "unwired")
	path := "/api/v1/runs/" + task.LatestRunID + "/events"

	resp := h.get(t, path, "viewer-tok", http.Header{"Accept": {"text/event-stream"}})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("unwired stream = %d, want 500", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", got)
	}
	if pd := decodeInto[httpx.ProblemDetails](t, resp); pd.Status != http.StatusInternalServerError {
		t.Errorf("problem status = %d, want 500", pd.Status)
	}
	// And the poll is untouched by the omission.
	if got := h.get(t, path, "viewer-tok", nil); got.StatusCode != http.StatusOK {
		t.Errorf("poll = %d, want 200", got.StatusCode)
	}
}

// Negotiation happens INSIDE the authorized handler, so every answer the poll
// gives for a run the caller may not see, the stream gives too.
func TestRunsAPI_ListRunEventsStreamStillEnforcesTheGrant(t *testing.T) {
	streams := &fakeStreamServer{}
	h := newHarnessWithStreams(t, streams)
	task := h.createTask(t, "admin-tok", "not yours")

	resp := h.get(t, "/api/v1/runs/"+task.LatestRunID+"/events", "outsider-tok", http.Header{
		"Accept": {"text/event-stream"},
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outsider stream = %d, want 403", resp.StatusCode)
	}
	if calls := streams.served(); len(calls) != 0 {
		t.Errorf("an unauthorized stream reached the port: %+v", calls)
	}
}

func TestRunsAPI_ListRunEventsRejectsAMalformedLastEventID(t *testing.T) {
	streams := &fakeStreamServer{}
	h := newHarnessWithStreams(t, streams)
	task := h.createTask(t, "admin-tok", "bad resume")

	resp := h.get(t, "/api/v1/runs/"+task.LatestRunID+"/events", "viewer-tok", http.Header{
		"Accept":        {"text/event-stream"},
		"Last-Event-ID": {"soon"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("junk resume = %d, want 400", resp.StatusCode)
	}
	pd := decodeInto[httpx.ProblemDetails](t, resp)
	if len(pd.Errors) != 1 || pd.Errors[0].Field != "Last-Event-ID" {
		t.Errorf("errors = %+v, want one blaming Last-Event-ID", pd.Errors)
	}
	if calls := streams.served(); len(calls) != 0 {
		t.Errorf("a malformed resume reached the port: %+v", calls)
	}
}

func TestAPI_GateDecisionRequiresApproveGrant(t *testing.T) {
	h := newHarness(t)
	// The runner requests the task and the approver decides it, so nothing
	// here depends on the shipped policy permitting self-approval.
	_, runID, _ := h.gatedRun(t, "runner-tok", h.repoID, "ship it")
	body := runsapi.GateDecisionRequest{Decision: "approve"}

	// THE ORDER OF THESE SUBTESTS IS LOAD-BEARING, and so is their sharing one
	// gate. The two refusals run first against a gate that is still waiting, and
	// the approval that follows only succeeds because neither of them wrote
	// anything — which is how "the grant is checked BEFORE the write" becomes an
	// observable property instead of an assertion about a status code. Reorder
	// them, give each its own run, or add t.Parallel, and a handler that decides
	// first and checks the grant afterwards passes every line below.
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
		// 2, not 1: the fixture has one revision round behind it, and approve
		// reports the generation of the attempt it DECIDED. A response echoing
		// the run's first generation, or a constant, is a different number.
		if out.Generation != 2 {
			t.Errorf("generation = %d, want 2: approve reports the generation decided", out.Generation)
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
	_, runID, _ := h.gatedRun(t, "approver-tok", h.repoID, "ship it")
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
	_, runID, _ := h.gatedRun(t, "approver-tok", h.repoID, "ship it")

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
	taskA, runA, stampsA := h.gatedRun(t, "approver-tok", h.repoID, "gate on A")
	taskB, runB, _ := h.gatedRun(t, "approver-b-tok", h.repoB, "gate on B")

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
		// Attempt and generation are both past 1 and DIFFERENT from each other,
		// which is the point of staging a spent revision round and of the gap
		// stageGate forces. At 1 they are indistinguishable from a hardcoded 1,
		// from the run's first generation, and from a column read off the wrong
		// row; equal to each other, they are indistinguishable from each other,
		// and a DTO that transposed them would satisfy every assertion here.
		want := runsapi.ApprovalResponse{
			RunID: runA.String(), TaskID: taskA.ID, ProjectID: h.projectID.String(),
			TaskTitle: "gate on A", NodeID: "gate", Attempt: 3, Generation: 2,
			RequesterID: h.approver.UserID.String(), WaitingSince: got.WaitingSince,
			// The approver requested this run and the shipped policy permits
			// self-approval, so they may decide it; gatedDoc has no review node,
			// so there is no summary to name.
			Evidence: runsapi.GateEvidenceResponse{
				ExaminedArtifactID: stampsA.livePatchID, ExaminedArtifactType: "patch",
				SummaryArtifactID: nil, CanDecide: true, CannotDecideReason: "",
				WallDeadline: got.Evidence.WallDeadline,
			},
		}
		// cmp.Diff rather than !=: Evidence holds a *string, and == would compare
		// it by ADDRESS — two decoded values that agree would still differ.
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("entry differs (-want +got):\n%s", diff)
		}
		// The deadline is excluded from the comparison above for the same reason
		// WaitingSince is, and pinned here against the value stageGate wrote.
		if !got.Evidence.WallDeadline.Equal(stampsA.wallDeadline) {
			t.Errorf("wall_deadline = %s, want the run's %s",
				got.Evidence.WallDeadline, stampsA.wallDeadline)
		}
		// WaitingSince is compared apart from the struct because time.Time
		// equality includes the location pointer, and the value has been through
		// JSON. The staged value is a distinct offset from every other timestamp
		// on the run, so reading the wrong column — the run's started_at, the
		// superseded attempt's created_at, the decision's acted_at — lands
		// somewhere else and fails here. IsZero alone only catches NULL.
		if !got.WaitingSince.Equal(stampsA.waitingSince) {
			t.Errorf("waiting_since = %s, want the live gate attempt's created_at %s",
				got.WaitingSince, stampsA.waitingSince)
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
// It asserts the SHAPE over staged rows, deliberately not the conveyor arc. A
// publication that actually reaches 'published' on a forge belongs to Task 13's
// TestGoldenSet_FullConveyorThroughTheAPI, which drives the real thing; there
// is no value in a second, fake version of it here. What it DOES need from the
// arc is two gate attempts with different verdicts, because that is the only
// shape in which "a decision lands on the attempt it was made on" can be wrong
// — see stageGate.
func TestAPI_RunProjectionCarriesTheGateFields(t *testing.T) {
	h := newHarness(t)
	// The runner requests, the approver decides: self_approval is then a
	// projected fact rather than a constant this fixture cannot help but be.
	// The superseded round was decided by the requester, so this one run carries
	// the marker in both states.
	task, runID, stamps := h.gatedRun(t, "runner-tok", h.repoID, "ship it")

	before := h.dbNow(t)
	resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), "approver-tok",
		runsapi.GateDecisionRequest{Decision: "approve", Note: "ship it"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decision = %d, want 200", resp.StatusCode)
	}
	after := h.dbNow(t)
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
		// The run has one revision round behind it, so every generation here is
		// a value the projection had to read rather than the 1 a default gives
		// away for free.
		if run.Generation != 2 {
			t.Errorf("run generation = %d, want 2", run.Generation)
		}
		if g := phaseAt(t, run, "gate", liveGateAttempt).Generation; g != 2 {
			t.Errorf("live gate phase generation = %d, want 2", g)
		}
		if g := phaseAt(t, run, "gate", 1).Generation; g != 1 {
			t.Errorf("superseded gate phase generation = %d, want 1: it keeps the "+
				"generation it ran in, it does not follow the run's", g)
		}
		if len(run.Artifacts) != 2 {
			t.Fatalf("artifacts = %+v, want the patch of each round", run.Artifacts)
		}
		// Ordered by created_at, so the superseded round comes first. The two
		// ordinals differ on both rows — the first round retried once — so a
		// projection that swapped the columns is caught here rather than passing
		// on a fixture where every attempt happens to equal its generation.
		if a := run.Artifacts[0]; a.Attempt != 2 || a.Generation != 1 {
			t.Errorf("older artifact = %+v, want attempt 2 generation 1", a)
		}
		if a := run.Artifacts[1]; a.Attempt != 3 || a.Generation != 2 {
			t.Errorf("newer artifact = %+v, want attempt 3 generation 2", a)
		}
	})

	t.Run("the decided gate phase carries who decided and what they examined", func(t *testing.T) {
		gate := phaseAt(t, run, "gate", liveGateAttempt)
		if gate.Decision == nil {
			t.Fatal("the decided gate phase carries no decision")
		}
		want := runsapi.GateDecisionSummary{
			Decision: "approve", Note: "ship it",
			ActedByID: h.approver.UserID.String(), ActedByEmail: "approver@example.com",
			ActedAt: gate.Decision.ActedAt, SelfApproval: false, ExaminedArtifactID: stamps.livePatchID,
		}
		if *gate.Decision != want {
			t.Errorf("decision = %+v, want %+v", *gate.Decision, want)
		}
		// ActedAt is excluded from the comparison above (time.Time equality
		// carries a location pointer through JSON) and the service picks it, so
		// it is bounded by two reads of the DATABASE clock taken either side of
		// the request. Comparing against a Go-side time.Now would be comparing
		// two unrelated clocks; IsZero alone passes for any wrong-but-set column,
		// including the acted_at of the round before.
		if got := gate.Decision.ActedAt; got.Before(before) || got.After(after) {
			t.Errorf("acted_at = %s, want inside [%s, %s]", got, before, after)
		}
	})

	// THE CENTRAL CLAIM of the projection, and the only one that needs both
	// rounds. Both gate attempts carry the same node id, so a join keyed on
	// node_id instead of phase_run_id smears one round's verdict over the other
	// and everything below still has a decision to find — it just has the wrong
	// one. Asserting the two verdicts differ is what makes that observable.
	t.Run("each gate attempt carries the verdict given on it and no other", func(t *testing.T) {
		live, prior := phaseAt(t, run, "gate", liveGateAttempt), phaseAt(t, run, "gate", 1)
		if live.Decision == nil || prior.Decision == nil {
			t.Fatalf("both gate attempts must carry their own decision: live=%+v prior=%+v",
				live.Decision, prior.Decision)
		}
		if live.Decision.Decision != "approve" || prior.Decision.Decision != "request_changes" {
			t.Errorf("verdicts = live %q / prior %q, want approve / request_changes",
				live.Decision.Decision, prior.Decision.Decision)
		}
		// Every other projected field of the two decisions differs too, so a
		// smear is caught even by a reader that only looks at one of them.
		if prior.Decision.ActedByID != h.runner.UserID.String() ||
			prior.Decision.ActedByEmail != "runner@example.com" ||
			!prior.Decision.SelfApproval ||
			prior.Decision.ExaminedArtifactID != stamps.priorPatchID ||
			!prior.Decision.ActedAt.Equal(stamps.priorActedAt) {
			t.Errorf("superseded decision = %+v, want the requester's self-approved "+
				"request_changes on %s at %s", *prior.Decision, stamps.priorPatchID, stamps.priorActedAt)
		}
		// Counting catches the other way of getting the key wrong: a join on
		// run_id hands the same decision to every phase of the run, gates and
		// implementations alike.
		decided, gates := 0, 0
		for _, p := range run.Phases {
			if p.Decision != nil {
				decided++
			}
			if p.NodeID == "gate" {
				gates++
			}
		}
		if gates != 2 {
			t.Fatalf("gate attempts = %d of %+v, want the fixture's two", gates, run.Phases)
		}
		if decided != 2 {
			t.Errorf("phases carrying a decision = %d of %d, want exactly the two gate attempts",
				decided, len(run.Phases))
		}
		// All three implementation attempts, by ordinal: the lookup itself fails
		// if the projection reported an attempt other than the one on the row.
		for _, attempt := range []int32{1, 2, 3} {
			if impl := phaseAt(t, run, "impl", attempt); impl.Decision != nil {
				t.Errorf("impl attempt %d carries a decision: %+v", attempt, impl.Decision)
			}
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
		_, ownRun, _ := h.gatedRun(t, "approver-tok", h.repoID, "my own gate")
		if resp := h.do(t, http.MethodPost, decisionPath(ownRun, "gate"), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusOK {
			t.Fatalf("self-approval = %d, want 200", resp.StatusCode)
		}
		gate := phaseAt(t, getRun(t, ownRun), "gate", liveGateAttempt)
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

// phaseAt selects a phase by node id AND attempt. Node id alone is ambiguous
// once a run has a revision behind it — that is the whole reason the projection
// keys decisions on phase_run_id — so a lookup that takes the first match would
// quietly assert against whichever row the ordering happened to put first.
func phaseAt(t *testing.T, run runsapi.RunResponse, nodeID string, attempt int32) runsapi.PhaseResponse {
	t.Helper()
	for _, p := range run.Phases {
		if p.NodeID == nodeID && p.Attempt == attempt {
			return p
		}
	}
	t.Fatalf("no phase for node %q attempt %d in %+v", nodeID, attempt, run.Phases)
	return runsapi.PhaseResponse{}
}

// dbNow reads the DATABASE clock. Timestamps this suite bounds are written by
// database now(), and the harness injects an app clock pinned to a fixed date
// in 2026 — so bounding them with a Go-side time.Now would compare two
// unrelated clocks and fail or pass for reasons that have nothing to do with
// the projection.
func (h *harness) dbNow(t *testing.T) time.Time {
	t.Helper()
	var now time.Time
	if err := h.pool.QueryRow(context.Background(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}
