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
// What is faked: runs.Executor, and nothing else. Raw SQL insertion of a gate
// row is BANNED and this file must never grow one — appendEvents allocates the
// ids the resume path depends on, so a hand-written INSERT desynchronises the
// counter and leaves later resume behaviour broken in a way no assertion in
// this lane would catch.
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

// e2eWorkflow is the smallest definition that can reach a waiting gate: one
// agentic node the fake executor answers, and a gate examining its patch.
// A gate cannot be a root node — workflows.validateConsumption requires it to
// examine exactly one plan or patch — which is the whole reason this lane
// substitutes the executor rather than inserting a gate row.
const e2eWorkflow = `{
  "schema": 1,
  "nodes": [
    {"id": "impl", "type": "implement", "harness": "e2e", "instructions": "Write the change."},
    {"id": "gate", "type": "gate"}
  ],
  "edges": [{"from": "impl", "to": "gate", "artifact": "patch"}]
}`

func TestWebE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 1. The forge stub. resolveIntake calls ResolveHead on the way to every
	//    task, and the GitHub client wants {"sha": ...} from
	//    GET {base}/repos/{slug}/commits/{branch}.
	forgeStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"sha": "0000000000000000000000000000000000000000"})
	}))
	defer forgeStub.Close()

	// 2. Bind FIRST, then configure. The port must be known before AppOrigin
	//    is set: CheckOrigin compares the browser's Origin header to it by
	//    exact equality on the login POST, and a browser always sends the port.
	//    Port 0 lets the OS pick, so the lane never collides.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener: %v", err)
	}
	baseURL := "http://" + ln.Addr().String()
	cfg := e2eConfig(t, testdb.NewURL(t), forgeStub.URL, baseURL)

	// 3. Boot the production server with the ONE substitution. The listener is
	//    a test seam, not a second fake: production Serve receives the same
	//    *net.TCPListener it would have bound itself.
	logBuf := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- runServeWith(ctx, cfg, e2eLogger(logBuf), serveOptions{
			executor: fake.NewExecutor(),
			listener: ln,
		})
	}()
	// The socket accepts the moment it is bound, so a connection would queue in
	// the backlog while migrations run and Playwright would see a hang rather
	// than a refusal. Wait for the server to say it is serving.
	awaitListening(t, logBuf, done)

	// 4. Bootstrap the admin THROUGH the production path, then seed the assets
	//    the slice needs. Every one of these goes through the real services.
	seed := e2eSeed(ctx, t, cfg)

	// 5. The control server. Playwright calls this BETWEEN assertions, which is
	//    what makes beat 2 happen while the browser is watching. It is part of
	//    the harness and never part of caesar's routes.
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fixture/open-gate" {
			http.NotFound(w, r)
			return
		}
		// CreateTask -> insertRun (pending) -> the Orchestrator's
		// startPendingRuns materializes the root -> a worker claims it -> the
		// FAKE executor returns a patch -> the orchestrator records it and
		// materializes the GATE, inserted 'waiting_decision', with its
		// gate.waiting event in the same appendEvents transaction.
		if _, _, err := seed.runsSvc.CreateTask(r.Context(), seed.admin, runs.NewTask{
			RepoID: seed.repoID, WorkflowID: seed.workflowID, WorkflowVersion: seed.workflowVersion,
			Title: "E2E approval", Inputs: map[string]any{},
		}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer control.Close()

	// 6. Hand it to Playwright. By PATH, never `npx`: npx reaches the public
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
	runsSvc            *runs.Service
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
	if _, err := assetsSvc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "e2e", Kind: assets.HarnessCodex, ImageRef: "unused-by-the-fake-executor",
		ProviderEndpoint: "http://localhost:1", CredentialID: modelCred.ID,
	}); err != nil {
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
		principalSource{svc: identitySvc}, runs.Params{Snapshot: runs.DefaultSnapshot()})

	return e2eFixture{
		admin: admin, email: email, password: password,
		repoID: repo.ID, workflowID: wf.ID, workflowVersion: version.Version, runsSvc: runsSvc,
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
