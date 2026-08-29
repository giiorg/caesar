//go:build webe2e && integration

package app

// The web E2E harness. It boots the PRODUCTION runServe against a real
// Postgres, stands up a forge stub for the one outbound HTTP call task intake
// makes, and exposes a control server Playwright calls between assertions so
// the gate opens WHILE the browser is watching.
//
// What is real here: Routes, every handler, the Orchestrator, materializeNode,
// appendEvents (ordered ids under the counter lock, the outbox envelope and the
// NOTIFY, all in one transaction), the tailer, the hub and the SSE wire.
//
// What is faked: runs.Executor and the forge stub named above — which answers
// with the head SHA of the same bare repository the executor is pointed at —
// and nothing else. Raw SQL insertion of a gate
// row is BANNED and this file must never grow one — appendEvents allocates the
// ids the resume path depends on, so a hand-written INSERT desynchronises the
// counter and leaves later resume behaviour broken in a way no assertion in
// this lane would catch. That ban covers the RESET as much as the setup:
// /fixture/quiesce decides leftover gates through runs.Service rather than
// deleting their rows.
//
// The executor is faked but its OUTPUT is not approximated: it is pointed at a
// real bare repository so the patch it returns is the canonical wire form,
// which is the only form the web client can decode. See step 1.
//
// The build tag names BOTH webe2e and integration, which the plan's `//go:build
// webe2e` did not. This file reaches for syncBuffer, which lives in
// serve_integration_test.go behind the integration tag, so `go test -tags
// webe2e` alone does not compile — and it fails as an undefined symbol, which
// reads as a broken package rather than a missing tag. Every lane that touches
// this file (task web:e2e, and backend:vet/analyze/vuln) sets both.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/config"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// e2eWorkflow is the smallest definition that reaches a waiting gate AND
// produces the evidence the approval surface renders: an implement node the
// fake answers with a patch, a review node that answers with a schema-2
// verdict, and a gate examining the patch with the verdict beside it.
// A gate cannot be a root node — workflows.validateConsumption requires it to
// examine exactly one plan or patch — which is the whole reason this lane
// substitutes the executor rather than inserting a gate row.
//
// SCHEMA 2, deliberately. This lane is 12a's acceptance path, so it must run
// with payload validation ON — a lane pinned at schema 1 would prove the
// screens work against payloads nothing checks.
const e2eWorkflow = `{
  "schema": 2,
  "nodes": [
    {"id": "impl", "type": "implement", "harness": "e2e", "instructions": "Write the change."},
    {"id": "review", "type": "review", "harness": "e2e", "instructions": "Review the change."},
    {"id": "gate", "type": "gate", "on_request_changes": {"target": "impl", "max_rounds": 2}}
  ],
  "edges": [
    {"from": "impl", "to": "review", "artifact": "patch"},
    {"from": "impl", "to": "gate", "artifact": "patch"},
    {"from": "review", "to": "gate", "artifact": "review_verdict"}
  ]
}`

func TestWebE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. A REAL BARE REPOSITORY, because the gate's evidence has to be a patch
	//    the client can decode.
	//
	//    The fake executor has two implement branches. Without a registered
	//    remote it emits the historical SYNTHETIC patch, whose `diff` is raw
	//    diff text — and the wire form is base64 (`fake/git.go:71-85`,
	//    `collect.go`), because a patch is arbitrary bytes and JSON string
	//    encoding would mangle a file in a legacy encoding silently. The web
	//    client decodes accordingly (`patch/decode-patch.ts:37-48`, atob then
	//    TextDecoder) and returns an EMPTY patch when that throws.
	//
	//    MEASURED, and it is why this exists: with the synthetic patch the
	//    approval surface rendered `0 files · +0 · −0` and no file rows at all
	//    for a patch that adds a line, so the gate screen showed nothing to
	//    examine. A lane whose acceptance path is "open the gate and read what
	//    it examines" cannot run against a payload shape production never
	//    produces — the executor is the ONE thing faked here, and a fake that
	//    differs from the real thing in the field under test is not a fake, it
	//    is a different subject.
	//
	//    Registering the remote takes the publishable branch: the canonical
	//    base64 wire form, a real `git write-tree` id, and one added file named
	//    per node (`CAESAR-<node_id>.md`).
	bareDir := t.TempDir()
	baseSHA, err := fake.InitBareRepo(bareDir)
	if err != nil {
		t.Fatalf("bare repo: %v", err)
	}
	bare := filepath.Join(bareDir, "bare.git")
	executor := fake.NewExecutor()

	// 2. The forge stub. resolveIntake calls ResolveHead on the way to every
	//    task, and the GitHub client wants {"sha": ...} from
	//    GET {base}/repos/{slug}/commits/{branch}. It answers the BARE REPO'S
	//    head rather than a zero sha: `approvedTree` checks that commit out to
	//    build the tree id the patch pins, and a sha the repository does not
	//    contain fails the implement node instead of the gate.
	forgeStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": baseSHA})
	}))
	defer forgeStub.Close()

	// 3. Bind FIRST, then configure. The port must be known before AppOrigin
	//    is set: CheckOrigin compares the browser's Origin header to it by
	//    exact equality on the login POST, and a browser always sends the port.
	//    Port 0 lets the OS pick, so the lane never collides.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener: %v", err)
	}
	baseURL := "http://" + ln.Addr().String()
	cfg := e2eConfig(t, testdb.NewURL(t), forgeStub.URL, baseURL)

	// 4. Boot the production server with the ONE substitution. The listener is
	//    a test seam, not a second fake: production Serve receives the same
	//    *net.TCPListener it would have bound itself.
	logBuf := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runServeWith(ctx, cfg, e2eLogger(logBuf), serveOptions{
			executor: executor,
			listener: ln,
		})
	}()
	// The socket accepts the moment it is bound, so a connection would queue in
	// the backlog while migrations run and Playwright would see a hang rather
	// than a refusal. Wait for the server to say it is serving.
	awaitListening(t, logBuf, done)

	// 5. Bootstrap the admin THROUGH the production path, then seed the assets
	//    the slice needs. Every one of these goes through the real services.
	seed := e2eSeed(ctx, t, cfg)
	// The repo id only exists once the seed has run, and the first execution
	// only happens once Playwright calls the control server — so telling the
	// executor here is in time. Without this line every implement node falls
	// back to the synthetic patch and the gate screen has nothing to show.
	executor.Remote(seed.repoID, bare)

	// 6. The control server. Playwright calls this BETWEEN assertions, which is
	//    what makes beat 2 happen while the browser is watching. It is part of
	//    the harness and never part of caesar's routes.
	control := httptest.NewServer(e2eFixtures(seed))
	defer control.Close()

	// 7. Hand it to Playwright. By PATH, never `npx`: npx reaches the public
	//    registry when the binary is missing locally, so a lane that lost its
	//    node_modules would silently run a DIFFERENT playwright than the one
	//    web/package.json pins.
	cmd := exec.CommandContext(ctx, "./node_modules/.bin/playwright", "test")
	cmd.Dir = "../../../web"
	cmd.Env = append(os.Environ(),
		"CAESAR_BASE_URL="+baseURL,
		"CAESAR_CONTROL_URL="+control.URL,
		"CAESAR_EMAIL="+seed.email,
		"CAESAR_PASSWORD="+seed.password,
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		// Read the failure in this order: Playwright's own output (already on
		// stdout above), then what the database reached, then the server log.
		t.Fatalf("playwright: %v\n\nphase_runs:\n%s\nserver log:\n%s",
			err, e2eDiagnose(ctx, t, cfg.Database.URL), logBuf.String())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("runServe did not shut down")
	}
}

// e2eFixtures is the control surface, and every endpoint on it goes through
// runs.Service. The file header's ban on raw SQL covers the RESET as much as
// the setup: `/fixture/quiesce` decides leftover gates through DecideGate
// rather than deleting or updating their rows, because a hand-written write
// desynchronises the id counter appendEvents allocates and leaves later resume
// behaviour broken in a way no assertion in this lane would catch.
//
// WHY A RESET EXISTS AT ALL. One server and one database serve the whole
// Playwright run, so the approvals inbox is shared mutable state across spec
// files: a spec that opens a gate and does not decide it leaves that gate
// waiting for every spec that follows. `/fixture/quiesce` is what lets a spec
// assert "the badge shows exactly 1" instead of a count relative to whatever
// the previous file left behind. It is paired with `workers: 1` in
// playwright.config.ts — with two workers, each one's reset would clear the
// other's gate.
func e2eFixtures(seed e2eFixture) http.Handler {
	mux := http.NewServeMux()

	// The gate opener, answering 202 exactly as it always has. What is new is
	// the BODY: a spec that cannot name its own run cannot tell its gate card
	// from a leftover one, and `page.getByTestId('gate-card')` strict-fails the
	// moment two gates wait. The existing approvals.spec asserts only the
	// status, so a body is compatible with it.
	mux.HandleFunc("POST /fixture/open-gate", func(w http.ResponseWriter, r *http.Request) {
		// CreateTask -> insertRun (pending) -> the Orchestrator's
		// startPendingRuns materializes the root -> a worker claims it -> the
		// FAKE executor returns a patch -> the orchestrator records it and
		// materializes the GATE, inserted 'waiting_decision', with its
		// gate.waiting event in the same appendEvents transaction.
		_, run, err := seed.runsSvc.CreateTask(r.Context(), seed.admin, runs.NewTask{
			RepoID: seed.repoID, WorkflowID: seed.workflowID, WorkflowVersion: seed.workflowVersion,
			Title: "E2E approval", Inputs: map[string]any{},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		e2eJSON(w, http.StatusAccepted, map[string]any{"run_id": run.ID.String()})
	})

	// What the create-task form's three selects will show. LABELS, NOT IDS:
	// the form renders native <select>s whose options are labelled by project
	// name, repo slug and workflow name (new-task-form.tsx:150,168,182), and a
	// spec drives them with `selectOption({ label })`. Serving the labels from
	// the seed rather than hard-coding them in the spec is what makes renaming
	// the seed repo a red lane instead of a silent miss.
	mux.HandleFunc("GET /fixture/seed", func(w http.ResponseWriter, _ *http.Request) {
		e2eJSON(w, http.StatusOK, map[string]any{
			"project": seed.projectName, "repo": seed.repoSlug, "workflow": seed.workflowName,
			"harness": seed.harnessName,
		})
	})

	// THE BACKEND ORACLE. "A new generation opened" is a backend fact, and a
	// spec that read it only off the run header would pass against a screen
	// that re-rendered generation 1 under a new label — which is a mutant this
	// task measured, not a hypothetical.
	mux.HandleFunc("GET /fixture/run/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		run, phases, _, err := seed.runsSvc.GetRun(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		rows := make([]map[string]any, 0, len(phases))
		for _, p := range phases {
			rows = append(rows, map[string]any{
				"node_id": p.NodeID, "status": string(p.Status),
				"attempt": p.Attempt, "generation": p.Generation,
			})
		}
		e2eJSON(w, http.StatusOK, map[string]any{
			"generation": run.Generation, "status": string(run.Status), "phases": rows,
		})
	})

	// The same oracle for the workflow the EDITOR authors. `latest_version` is a
	// database fact, and a version view rendering "v1" is a statement about a
	// template.
	//
	// IT GOES THROUGH THE CONTROL SERVER RATHER THAN THE APP'S OWN API, and that
	// is measured rather than stylistic: Playwright's `page.request` shares the
	// browser context's cookie jar but applies `Secure` strictly, while the
	// browser exempts localhost — so the `__Host-` session cookie rides every
	// request the PAGE makes and none that the Node-side client makes. The spec
	// asking `GET /api/v1/workflows/{id}` that way got a 401 with the UI
	// otherwise green. This reads through workflows.Service, which is stronger
	// anyway: no HTTP layer between the assertion and the row.
	mux.HandleFunc("GET /fixture/workflow/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		wf, err := seed.workflowsSvc.GetWorkflow(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		e2eJSON(w, http.StatusOK, map[string]any{
			"name": wf.Name, "latest_version": wf.LatestVersion,
		})
	})

	mux.HandleFunc("POST /fixture/quiesce", func(w http.ResponseWriter, r *http.Request) {
		decided, err := e2eQuiesce(r.Context(), seed)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		e2eJSON(w, http.StatusOK, map[string]any{"decided": decided})
	})

	return mux
}

func e2eJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// e2eQuiesce returns the world to "no gate is waiting and no run is in
// flight", and reports how many gates it had to decide to get there.
//
// It APPROVES rather than rejects: approve is the decision that finishes this
// workflow, while request_changes would open another round and reject would
// leave every quiesced run `failed` — a status the specs' own screens render,
// so the reset would be seeding the fixtures of whichever spec ran next.
//
// The loop is over BOTH conditions and not just the gates. A run the previous
// spec left mid-revision is still `running`, and it will materialize a gate
// seconds later — inside the next spec's assertions, moving a badge that spec
// is counting. Waiting for the run list to go quiet is what makes the count a
// spec reads at its first beat a count nothing else can change.
func e2eQuiesce(ctx context.Context, seed e2eFixture) (int, error) {
	deadline := time.Now().Add(90 * time.Second)
	decided := 0
	for {
		gates, err := seed.runsSvc.ListWaitingGates(ctx, nil, true)
		if err != nil {
			return decided, fmt.Errorf("quiesce: list waiting gates: %w", err)
		}
		for _, g := range gates {
			if _, err := seed.runsSvc.DecideGate(ctx, seed.admin, runs.GateDecisionInput{
				RunID: g.RunID, NodeID: g.NodeID, Decision: runs.DecisionApprove,
				Note: "quiesced between specs",
			}); err != nil {
				return decided, fmt.Errorf("quiesce: decide %s/%s: %w", g.RunID, g.NodeID, err)
			}
			decided++
		}
		live, err := e2eLiveRuns(ctx, seed)
		if err != nil {
			return decided, err
		}
		if len(gates) == 0 && len(live) == 0 {
			return decided, nil
		}
		if time.Now().After(deadline) {
			return decided, fmt.Errorf("quiesce: still live after 90s: %s", strings.Join(live, ", "))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// e2eLiveRuns names every run that has not settled. One page of 100 is the
// whole world here: the specs create a handful of runs between them, and a
// lane that ever exceeded the page would report a quiet world that is not one
// — which fails loudly at the next spec's first beat rather than silently.
func e2eLiveRuns(ctx context.Context, seed e2eFixture) ([]string, error) {
	rows, _, err := seed.runsSvc.ListRuns(ctx, runs.RunListFilter{All: true, Limit: 100})
	if err != nil {
		return nil, fmt.Errorf("quiesce: list runs: %w", err)
	}
	var live []string
	for _, r := range rows {
		switch r.Status {
		case runs.RunPending, runs.RunRunning, runs.RunWaiting:
			live = append(live, fmt.Sprintf("%s=%s", r.ID, r.Status))
		}
	}
	return live, nil
}

// e2eLogger writes the server's log to a buffer awaitListening reads AND to
// stderr, which is what makes a failing lane diagnosable in CI.
func e2eLogger(buf *syncBuffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.MultiWriter(buf, os.Stderr), nil))
}

// awaitListening blocks until runServe reports that it is serving, or fails
// the test. It watches done as well as the log so that a boot failure — a
// migration that will not apply, an unreachable database — surfaces as that
// error rather than as a 60-second timeout that says nothing.
func awaitListening(t *testing.T, log *syncBuffer, done <-chan error) {
	t.Helper()
	want := `"` + msgListening + `"`
	deadline := time.Now().Add(60 * time.Second)
	for {
		if strings.Contains(log.String(), want) {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("runServe returned before listening: %v\n%s", err, log.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never reported listening:\n%s", log.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// e2eConfig builds the server configuration.
//
// appOrigin is the browser-visible base URL, and it must be character-for-
// character what Playwright's Origin header will carry — CheckOrigin does not
// normalize.
//
// This struct omits Runner entirely, which leaves Cpus at 0 and PidsLimit
// below the floor that config.validate() rejects. That is FINE and is not an
// oversight: validate() is called only from config.Load, and nothing here goes
// through Load — the same shape serve_integration_test.go has always used.
// Stated because it looks like a bug on every reading.
func e2eConfig(t *testing.T, dbURL, forgeBase, appOrigin string) config.Config {
	t.Helper()
	return config.Config{
		Server:   config.Server{Addr: "127.0.0.1:0", ShutdownTimeout: config.Duration(10 * time.Second), AppOrigin: appOrigin},
		Database: config.Database{URL: dbURL},
		Log:      config.Log{Level: "info"},
		Auth:     config.Auth{SessionIdleTTL: config.Duration(24 * time.Hour), SessionAbsoluteTTL: config.Duration(24 * time.Hour)},
		Security: config.Security{MasterKey: "e2e:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		// A poll interval well under the spec's 30s timeout, so the gate
		// materializes while Playwright is still watching rather than after it
		// has given up. This is the one knob that decides whether this lane is
		// fast or flaky.
		Orchestrator: config.Orchestrator{
			Workers: 2, PollInterval: config.Duration(200 * time.Millisecond),
			LeaseTTL: config.Duration(time.Minute), HeartbeatInterval: config.Duration(10 * time.Second),
			OutboxRetention: config.Duration(time.Hour),
		},
		// Only the API bases matter: nothing in this lane clones or pushes, and
		// resolveIntake's ResolveHead is the single outbound call.
		Forge: config.Forge{
			GitHubBaseURL: forgeBase, GitLabBaseURL: forgeBase,
			GitHubCloneBaseURL: forgeBase, GitLabCloneBaseURL: forgeBase,
			PublishWorkDir: t.TempDir(), RequestTimeout: config.Duration(5 * time.Second),
		},
		Realtime: config.Realtime{
			HeartbeatInterval: config.Duration(20 * time.Second), PollFallback: config.Duration(time.Second),
			GrantRecheck: config.Duration(30 * time.Second), SubscriberQueue: 64,
			ReplayBatch: 500, SnapshotEvents: 500,
		},
	}
}

type e2eFixture struct {
	admin              authn.Principal
	email, password    string
	repoID, workflowID uuid.UUID
	workflowVersion    int32
	// What the create-task form's three selects LABEL their options with, so
	// `/fixture/seed` can serve them and the Path-2 spec can pick by label
	// instead of hard-coding names this file owns.
	// …and, since Task 28, what the workflow EDITOR's harness picker labels its
	// options with: a definition authored through the UI names a harness, and
	// `checkHarnesses` refuses an unregistered one at intake.
	projectName, repoSlug, workflowName, harnessName string
	runsSvc                                          *runs.Service
	workflowsSvc                                     *workflows.Service
}

// e2eSeed builds the world through the REAL services — the same doors the admin
// CLI and the HTTP handlers use. Nothing here writes SQL directly.
func e2eSeed(ctx context.Context, t *testing.T, cfg config.Config) e2eFixture {
	t.Helper()
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	clk := clock.System{}
	rec := audit.NewRecorder(clk)
	keys, err := secrets.ParseKeyset(cfg.Security.MasterKey)
	if err != nil {
		t.Fatal(err)
	}
	identitySvc := identity.NewService(pool, clk, rec, identity.Params{
		SessionIdleTTL: cfg.Auth.SessionIdleTTL.Std(), SessionAbsoluteTTL: cfg.Auth.SessionAbsoluteTTL.Std(),
	})
	// At least MinPasswordLen (12) bytes, or Bootstrap rejects it.
	const email, password = "e2e@caesar.test", "a long enough e2e password"
	user, err := identitySvc.Bootstrap(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	admin := authn.Principal{UserID: user.ID, Email: user.Email, Role: authn.RoleAdmin}

	assetsSvc := assets.NewService(pool, clk, rec, keys)
	workflowsSvc := workflows.NewService(pool, clk, rec)

	project, err := assetsSvc.CreateProject(ctx, admin, "e2e", "")
	if err != nil {
		t.Fatal(err)
	}
	modelCred, err := assetsSvc.CreateCredential(ctx, admin, "e2e-model", assets.CredentialModelKey, []byte("sk-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := assetsSvc.CreateCredential(ctx, admin, "e2e-forge", assets.CredentialForgeToken, []byte("ghp-e2e"))
	if err != nil {
		t.Fatal(err)
	}
	// The workflow names harness "e2e", and checkHarnesses resolves every
	// agentic node's harness at intake — an unregistered one is a 400 at
	// CreateTask, not a hang, which is worth knowing when this lane fails.
	harness, err := assetsSvc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "e2e", Kind: assets.HarnessCodex, ImageRef: "unused-by-the-fake-executor",
		ProviderEndpoint: "http://localhost:1", CredentialID: modelCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := assetsSvc.CreateRepo(ctx, admin, project.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "caesar/e2e", DefaultBranch: "main", CredentialID: forgeCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflowsSvc.CreateWorkflow(ctx, admin, "e2e", "")
	if err != nil {
		t.Fatal(err)
	}
	// The version number is READ BACK rather than assumed to be 1: CreateTask
	// resolves the definition by (workflow, version), so a hardcoded 1 that
	// ever stopped matching would surface as a 404 at intake.
	version, _, err := workflowsSvc.CreateVersion(ctx, admin, wf.ID, []byte(e2eWorkflow))
	if err != nil {
		t.Fatal(err)
	}

	// A SECOND runs.Service over the same pool, for the control server only.
	// The one inside runServe owns the orchestrator; this one owns intake. They
	// share the database, which is the only state that matters.
	runsSvc := runs.NewService(pool, clk, rec,
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		newForgeAdapter(assetsSvc, forgeEndpointsFrom(cfg.Forge), &http.Client{Timeout: 5 * time.Second}),
		gitPublisher{assets: assetsSvc, workDir: cfg.Forge.PublishWorkDir, endpoints: forgeEndpointsFrom(cfg.Forge)},
		principalSource{svc: identitySvc}, runs.Params{
			Snapshot: runs.DefaultSnapshot(),
			Labels:   runsLabelSource{assets: assetsSvc, workflows: workflowsSvc},
		})

	return e2eFixture{
		admin: admin, email: email, password: password,
		repoID: repo.ID, workflowID: wf.ID, workflowVersion: version.Version,
		projectName: project.Name, repoSlug: repo.Slug, workflowName: wf.Name,
		harnessName:  harness.Name,
		runsSvc:      runsSvc,
		workflowsSvc: workflowsSvc,
	}
}

// e2eDiagnose reports what the database thinks the run reached. It is called
// only on failure, where "the badge never moved" is the symptom and the phase
// rows are the first thing worth knowing: a gate that never reached
// waiting_decision is a backend or seed fault, and a gate that did reach it is
// a client fault.
func e2eDiagnose(ctx context.Context, t *testing.T, dbURL string) string {
	t.Helper()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return "diagnose: " + err.Error()
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT node_id, status FROM phase_runs ORDER BY created_at`)
	if err != nil {
		return "diagnose: " + err.Error()
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var node, status string
		if err := rows.Scan(&node, &status); err != nil {
			return "diagnose: " + err.Error()
		}
		fmt.Fprintf(&b, "  phase_run %s = %s\n", node, status)
	}
	if b.Len() == 0 {
		return "  no phase_runs at all — intake never created a run\n"
	}
	return b.String()
}
