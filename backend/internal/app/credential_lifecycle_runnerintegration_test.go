//go:build runnerintegration

package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
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
	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
	"github.com/giiorg/caesar/backend/internal/runs/runner"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// The credential the whole lifecycle runs on: a Codex auth.json, and the
// refreshed one the scripted agent writes over it. Same account either way —
// an identity change is a quarantine, which is a different story (Task 9).
const (
	lifecycleStoredAuth    = `{"tokens":{"access_token":"at-stored","refresh_token":"rt-stored","account_id":"acct-lifecycle"}}`
	lifecycleRefreshedAuth = `{"tokens":{"access_token":"at-refreshed","refresh_token":"rt-refreshed","account_id":"acct-lifecycle"}}`
)

// lifecycleAgentScript is the CLI half of the story: refresh the credential in
// place — the one thing a materialized auth file exists to allow — then hold
// the attempt open so recovery meets a running container.
func lifecycleAgentScript(tail string) string {
	return "set -e\n" +
		"cat > \"$CODEX_HOME/auth.json\" <<'EOF'\n" + lifecycleRefreshedAuth + "\nEOF\n" +
		"echo lifecycle > /workspace/LIFECYCLE.md\n" +
		tail + "\n"
}

// lifecycleEnv is one isolated world: its own database, its own credential,
// and a runner over the real daemon whose agent runs the given script.
type lifecycleEnv struct {
	pool      *pgxpool.Pool
	assets    *assets.Service
	runs      *runs.Service
	admin     authn.Principal
	credID    uuid.UUID
	repoID    uuid.UUID
	wfID      uuid.UUID
	version   int32
	dataRoot  string
	rnr       *runner.Runner
	engine    *dockerapi.Client
	egressNet string
	log       *slog.Logger
}

func newLifecycleEnv(t *testing.T, engine *dockerapi.Client, repoDir, headSHA, script string) *lifecycleEnv {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testdb.NewURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	clk := clock.System{}
	rec := audit.NewRecorder(clk)
	keys, err := secrets.ParseKeyset("test:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	identitySvc := identity.NewService(pool, clk, rec, identity.Params{
		SessionIdleTTL: 14 * 24 * time.Hour, SessionAbsoluteTTL: 90 * 24 * time.Hour,
	})
	adminUser, err := identitySvc.Bootstrap(ctx, "root@example.com", "a long enough password")
	if err != nil {
		t.Fatal(err)
	}
	admin := authn.Principal{UserID: adminUser.ID, Email: adminUser.Email, Role: authn.RoleAdmin}

	assetsSvc := assets.NewService(pool, clk, rec, keys)
	workflowsSvc := workflows.NewService(pool, clk, rec)
	heads := map[uuid.UUID]string{}
	runsSvc := runs.NewService(pool, clk, rec,
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		fake.Forge{Heads: heads}, runs.Params{Snapshot: runs.DefaultSnapshot()})

	project, err := assetsSvc.CreateProject(ctx, admin, "credential-lifecycle", "")
	if err != nil {
		t.Fatal(err)
	}
	authCred, err := assetsSvc.CreateCredential(ctx, admin, "codex-subscription",
		assets.CredentialAgentAuthFile, []byte(lifecycleStoredAuth))
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := assetsSvc.CreateCredential(ctx, admin, "github-bot", assets.CredentialForgeToken, []byte("ghp-x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assetsSvc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "cx", Kind: assets.HarnessCodex, ImageRef: e2eGitImage,
		ProviderEndpoint: "https://chatgpt.com/backend-api", CredentialID: authCred.ID,
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := assetsSvc.CreateRepo(ctx, admin, project.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "lifecycle/demo", DefaultBranch: "main", CredentialID: forgeCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	heads[repo.ID] = headSHA

	wf, err := workflowsSvc.CreateWorkflow(ctx, admin, "credential-lifecycle", "")
	if err != nil {
		t.Fatal(err)
	}
	version, _, err := workflowsSvc.CreateVersion(ctx, admin, wf.ID, []byte(`{
	  "schema": 1,
	  "nodes": [{"id": "impl", "type": "implement", "harness": "cx", "instructions": "Implement."}],
	  "edges": []
	}`))
	if err != nil {
		t.Fatal(err)
	}

	dataRoot := t.TempDir()
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	egressNet := "caesar-egress-life-" + uuid.NewString()[:8]
	rnr, err := runner.New(engine,
		scriptedResolver{inner: appHarnessResolver{svc: assetsSvc}, script: script},
		appCredentialOpener{svc: assetsSvc},
		appCredentialLeaser{svc: assetsSvc},
		localBareGit{dir: repoDir},
		runsSvc,
		runner.Options{
			DataRoot: dataRoot, CaesarImage: e2eCaesarImage, GitImage: e2eGitImage,
			EgressNetwork:       egressNet,
			WorkspaceQuotaBytes: 64 << 20, WorkspaceQuotaFiles: 2000,
			LogCapBytes: 256 << 10, LogFloodBytes: 1 << 20,
			FreeBytesWatermark: 1 << 20, QuotaPollInterval: 200 * time.Millisecond,
		}, log)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	started := time.Now().Unix()
	t.Cleanup(func() { e2eSweep(t, engine, egressNet, started) })

	return &lifecycleEnv{
		pool: pool, assets: assetsSvc, runs: runsSvc, admin: admin,
		credID: authCred.ID, repoID: repo.ID, wfID: wf.ID, version: version.Version,
		dataRoot: dataRoot, rnr: rnr, engine: engine, egressNet: egressNet, log: log,
	}
}

// startOrchestrator runs one queue driver until the returned stop is called.
// The phase lease is deliberately long-lived and heartbeated often: every case
// here is about the DEADLINE path, and a phase lease that expired underneath it
// would hand the attempt to the lease sweep instead.
func (e *lifecycleEnv) startOrchestrator(t *testing.T, exec runs.Executor, workers int) func() {
	t.Helper()
	orch := runs.NewOrchestrator(e.runs, exec, runs.AlwaysAdmit{}, runsAssetSource{svc: e.assets},
		runs.OrchestratorConfig{
			Workers: workers, PollInterval: 100 * time.Millisecond, LeaseTTL: 10 * time.Minute,
			HeartbeatInterval: 3 * time.Second, OutboxRetention: 168 * time.Hour,
		}, e.log)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = orch.Run(ctx) }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop
}

func (e *lifecycleEnv) newRun(t *testing.T, title string) runs.Run {
	t.Helper()
	_, run, err := e.runs.CreateTask(context.Background(), e.admin, runs.NewTask{
		RepoID: e.repoID, WorkflowID: e.wfID, WorkflowVersion: e.version, Title: title,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// expireWallDeadline pulls the run's deadline into the past through the
// DATABASE clock, which is the clock every deciding predicate reads. A worker
// already running keeps the deadline it was handed at claim time, so this is
// exactly the race the deadline sweep exists for.
func (e *lifecycleEnv) expireWallDeadline(t *testing.T, runID uuid.UUID) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE runs SET wall_deadline = now() - interval '1 second' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
}

func (e *lifecycleEnv) authFilePath(ref runs.AttemptRef) string {
	return filepath.Join(e.dataRoot, "auth", ref.String(), "auth.json")
}

func lifeScalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// lifeWaitFor polls rather than sleeping a guessed duration; every condition
// here is reached by a real container or a janitor tick.
func lifeWaitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func lifeWaitForRunStatus(t *testing.T, svc *runs.Service, runID uuid.UUID, want runs.RunStatus) runs.Run {
	t.Helper()
	var got runs.Run
	lifeWaitFor(t, fmt.Sprintf("run %s to reach %s", runID, want), func() bool {
		run, _, _, err := svc.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		got = run
		return run.Status == want
	})
	return got
}

// assertCredentialConcluded pins the shape every one of these cases ends in:
// the refreshed bytes are in the store at the next version, the receipt says
// which attempt put them there, and nobody holds the lease any more.
func assertCredentialConcluded(t *testing.T, e *lifecycleEnv, ref runs.AttemptRef, wantEpoch int64) {
	t.Helper()
	if v := lifeScalar[int64](t, e.pool, `SELECT secret_version FROM credentials WHERE id = $1`, e.credID); v != 2 {
		t.Errorf("credential secret_version = %d, want 2 — the CLI's refresh landed exactly once", v)
	}
	epoch := lifeScalar[int64](t, e.pool,
		`SELECT epoch FROM credential_sync_receipts WHERE credential_id = $1 AND attempt_ref = $2`,
		e.credID, ref.String())
	if epoch != wantEpoch {
		t.Errorf("sync receipt epoch = %d, want %d", epoch, wantEpoch)
	}
	if n := lifeScalar[int64](t, e.pool,
		`SELECT count(*) FROM credential_leases WHERE credential_id = $1`, e.credID); n != 0 {
		t.Errorf("%d credential leases survive, want none: the obligation is concluded", n)
	}
}

// assertNoAttemptContainers pins that nothing of the attempt's runtime is left:
// the agent above all, which is the container that could still be writing the
// credential this reconciliation just committed.
func assertNoAttemptContainers(t *testing.T, e *lifecycleEnv, ref runs.AttemptRef) {
	t.Helper()
	for _, name := range []string{ref.String(), ref.String() + "-egress"} {
		if _, err := e.engine.ContainerInspect(context.Background(), name); !dockerapi.IsNotFound(err) {
			t.Errorf("container %s survived reconciliation (inspect err = %v)", name, err)
		}
	}
}

// stalledExecutor is a worker that never reaches its own evaluation: the real
// runner does the whole attempt, and then the process stops being able to write
// the result down. It is how the crash window between "the pipeline finished"
// and "the result was persisted" is reached deterministically.
//
// Inspect deliberately reports a rich adoption. Nothing past the deadline may
// call it — and if anything does, the fabricated success shows up as a
// succeeded phase and an artifact row, which is exactly what the case asserts
// against.
type stalledExecutor struct {
	*runner.Runner
	finished  chan struct{}
	inspected chan struct{}
}

func newStalledExecutor(r *runner.Runner) *stalledExecutor {
	return &stalledExecutor{Runner: r, finished: make(chan struct{}), inspected: make(chan struct{}, 8)}
}

func (e *stalledExecutor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	res, err := e.Runner.Execute(ctx, req)
	close(e.finished)
	<-ctx.Done() // the process that would have evaluated this result is gone
	return res, err
}

func (e *stalledExecutor) Inspect(context.Context, runs.AttemptRef) (runs.InspectResult, error) {
	select {
	case e.inspected <- struct{}{}:
	default:
	}
	return runs.InspectResult{Completed: true, Result: runs.ExecutionResult{
		Artifacts: []runs.NewArtifact{{Type: runs.ArtifactKindPlan, Payload: map[string]any{"content": "adopted past the deadline"}}},
	}}, nil
}

func (e *stalledExecutor) adoptionAttempted() bool {
	select {
	case <-e.inspected:
		return true
	default:
		return false
	}
}

// The lifecycle proof (spec §6): every way an attempt's credential obligation
// can outlive its worker, and the one order all of them settle in — take
// custody, stop the runtime, sync back with its receipt, release, and only then
// let the run die.
func TestCredentialLifecycle_ObligationOutlivesTheWorker(t *testing.T) {
	if _, err := os.Stat(e2eDockerSocket); err != nil {
		t.Skipf("the credential lifecycle suite needs a Docker daemon at %s: %v", e2eDockerSocket, err)
	}
	engine := dockerapi.New(e2eDockerSocket)
	for _, ref := range []string{e2eCaesarImage, e2eGitImage} {
		if _, err := engine.ImageInspect(context.Background(), ref); err != nil {
			t.Skipf("image %s is missing (run `task image:build`): %v", ref, err)
		}
	}
	repoDir := t.TempDir()
	headSHA, err := fake.InitBareRepo(repoDir)
	if err != nil {
		t.Fatal(err)
	}

	// 1. The restart case: a worker dies mid-attempt, the run's deadline passes
	//    while nothing is running, and a FRESH process picks the run up. The
	//    terminal event may not precede the credential's own conclusion.
	t.Run("a restarted process terminalizes only after the obligation landed", func(t *testing.T) {
		e := newLifecycleEnv(t, engine, repoDir, headSHA, lifecycleAgentScript("sleep 600"))
		run := e.newRun(t, "restart after deadline")
		ref := runs.AttemptRef{RunID: run.ID, NodeID: "impl", Attempt: 1}

		stopFirst := e.startOrchestrator(t, e.rnr, 1)
		lifeWaitFor(t, "the agent to refresh its credential on disk", func() bool {
			raw, err := os.ReadFile(e.authFilePath(ref))
			return err == nil && strings.Contains(string(raw), "rt-refreshed")
		})
		// The outage: this process stops. Its cancellation-immune conclusion is
		// the last thing it does for the credential, and the phase row it never
		// evaluated is what the next process finds.
		stopFirst()
		if got := lifeScalar[string](t, e.pool,
			`SELECT status FROM phase_runs WHERE run_id = $1`, run.ID); got != "running" {
			t.Fatalf("phase status = %s, want a row left mid-flight by the dead process", got)
		}
		e.expireWallDeadline(t, run.ID)

		e.startOrchestrator(t, e.rnr, 1)
		final := lifeWaitForRunStatus(t, e.runs, run.ID, runs.RunDead)
		if final.Outcome.Code != runs.CodeWallDeadlineExceeded {
			t.Errorf("run outcome = %+v, want wall_deadline_exceeded", final.Outcome)
		}
		// Epoch 1: the dying worker concluded its own obligation on the way
		// out, which is what a cancellation-immune conclusion is for.
		assertCredentialConcluded(t, e, ref, 1)
		assertNoAttemptContainers(t, e, ref)
		// No receipt-before-terminal assertion here: the worker concluded
		// during stopFirst, before this test expired the deadline or started
		// the second orchestrator, so any such comparison would hold on the
		// test's own sequencing whatever the sweep does. Case 4 is where the
		// sweep is provably the committer, and it carries that assertion.
	})

	// 2. The crash between the lease commit and the manifest write (spec §2.3):
	//    no manifest, no container, and a phase that will never run again. Only
	//    the janitor can end this, and it must — a lease nobody concludes is an
	//    eternal credential_busy for every later run on that credential.
	t.Run("a lease with no manifest behind it is released by the janitor", func(t *testing.T) {
		e := newLifecycleEnv(t, engine, repoDir, headSHA, lifecycleAgentScript("true"))
		run := e.newRun(t, "crash between lease and manifest")
		ref := runs.AttemptRef{RunID: run.ID, NodeID: "impl", Attempt: 1}

		// A janitor-only process: it materializes the run's root phase and
		// sweeps, but never claims — the attempt this lease belongs to is over.
		e.startOrchestrator(t, e.rnr, 0)
		lifeWaitFor(t, "the root phase to be materialized", func() bool {
			return lifeScalar[int64](t, e.pool, `SELECT count(*) FROM phase_runs WHERE run_id = $1`, run.ID) == 1
		})
		if _, err := e.pool.Exec(context.Background(),
			`UPDATE phase_runs SET status = 'dead', finished_at = now() WHERE run_id = $1`, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := e.assets.AcquireCredentialLease(context.Background(), e.credID, ref.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(context.Background(),
			`UPDATE credential_leases SET heartbeat_at = now() - interval '1 hour' WHERE credential_id = $1`,
			e.credID); err != nil {
			t.Fatal(err)
		}

		lifeWaitFor(t, "the janitor to release the abandoned lease", func() bool {
			return lifeScalar[int64](t, e.pool,
				`SELECT count(*) FROM credential_leases WHERE credential_id = $1`, e.credID) == 0
		})
		// The whole point of releasing: the next claimant proceeds instead of
		// waiting on a lease nothing was ever going to conclude.
		if _, _, err := e.assets.AcquireCredentialLease(context.Background(), e.credID, "caesar-next-attempt"); err != nil {
			t.Errorf("the credential is still unusable after reconciliation: %v", err)
		}
	})

	// 3. Staleness is not authority: a lease whose heartbeat lapsed but whose
	//    phase is still live belongs to that phase's own machinery. Taking
	//    custody here would fence a worker doing exactly what it should.
	t.Run("a stale lease under a live phase is left alone", func(t *testing.T) {
		e := newLifecycleEnv(t, engine, repoDir, headSHA, lifecycleAgentScript("true"))
		run := e.newRun(t, "stale lease under a live phase")
		ref := runs.AttemptRef{RunID: run.ID, NodeID: "impl", Attempt: 1}

		e.startOrchestrator(t, e.rnr, 0)
		lifeWaitFor(t, "the root phase to be materialized", func() bool {
			return lifeScalar[int64](t, e.pool, `SELECT count(*) FROM phase_runs WHERE run_id = $1`, run.ID) == 1
		})
		if _, err := e.pool.Exec(context.Background(),
			`UPDATE phase_runs SET status = 'running', worker_id = 'w-live', claimed_at = now(),
			        leased_until = now() + interval '10 minutes' WHERE run_id = $1`, run.ID); err != nil {
			t.Fatal(err)
		}
		lease, _, err := e.assets.AcquireCredentialLease(context.Background(), e.credID, ref.String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(context.Background(),
			`UPDATE credential_leases SET heartbeat_at = now() - interval '1 hour' WHERE credential_id = $1`,
			e.credID); err != nil {
			t.Fatal(err)
		}

		// Several janitor ticks' worth of chances to get it wrong.
		time.Sleep(2 * time.Second)
		held := lifeScalar[int64](t, e.pool,
			`SELECT epoch FROM credential_leases WHERE credential_id = $1 AND attempt_ref = $2`,
			e.credID, ref.String())
		if held != lease.Epoch {
			t.Errorf("the lease of a live phase moved to epoch %d, want it untouched at %d", held, lease.Epoch)
		}
	})

	// 4. The deadline outranks a healthy phase lease (spec §2.8). The worker is
	//    alive and heartbeating, so the lease sweep can never see this row —
	//    only the deadline-fenced transition can, and it must, or the credential
	//    obligation would ride on a worker whose run is already over.
	t.Run("an expired deadline fences a healthy phase and settles its credential", func(t *testing.T) {
		e := newLifecycleEnv(t, engine, repoDir, headSHA, lifecycleAgentScript("sleep 600"))
		run := e.newRun(t, "deadline beats a healthy phase lease")
		ref := runs.AttemptRef{RunID: run.ID, NodeID: "impl", Attempt: 1}

		e.startOrchestrator(t, e.rnr, 1)
		lifeWaitFor(t, "the agent to refresh its credential on disk", func() bool {
			raw, err := os.ReadFile(e.authFilePath(ref))
			return err == nil && strings.Contains(string(raw), "rt-refreshed")
		})
		if !lifeScalar[bool](t, e.pool,
			`SELECT leased_until > now() FROM phase_runs WHERE run_id = $1`, run.ID) {
			t.Fatal("the phase lease already expired; this case is about the one the lease sweep cannot see")
		}
		e.expireWallDeadline(t, run.ID)

		final := lifeWaitForRunStatus(t, e.runs, run.ID, runs.RunDead)
		if final.Outcome.Code != runs.CodeWallDeadlineExceeded {
			t.Errorf("run outcome = %+v, want wall_deadline_exceeded", final.Outcome)
		}
		// Epoch 2: the reconciler took custody from a worker that was still
		// alive, and it — not that worker — committed the refresh.
		assertCredentialConcluded(t, e, ref, 2)
		assertNoAttemptContainers(t, e, ref)

		// The ordering guarantee, where the sweep is the one that owes it
		// (spec §2.8). Because the committer here is settleRunCredentials and
		// not the worker, these two timestamps are written by the same
		// goroutine on the same tick, and only the order of the calls decides
		// which comes first: the terminal event may not precede the receipt
		// that the run's own credential obligation was discharged.
		synced := lifeScalar[time.Time](t, e.pool,
			`SELECT occurred_at FROM audit_entries WHERE action = 'credential.synced' AND subject_id = $1`,
			e.credID.String())
		terminal := lifeScalar[time.Time](t, e.pool,
			`SELECT occurred_at FROM run_events WHERE run_id = $1 AND kind = $2`, run.ID, runs.EventRunDead)
		if !terminal.After(synced) {
			t.Errorf("the run died at %s, before its credential synced at %s: the terminal event must come last",
				terminal, synced)
		}
	})

	// 5. The sweep's cleanup-only path never adopts. A finished, adoptable
	//    attempt past the deadline has already lost its right to a result:
	//    FinishRun carries no deadline predicate, so an adoption here would
	//    mark the run succeeded and the terminalization would silently lose.
	t.Run("a completed attempt past the deadline is settled but never adopted", func(t *testing.T) {
		e := newLifecycleEnv(t, engine, repoDir, headSHA, lifecycleAgentScript("true"))
		run := e.newRun(t, "exit zero past the deadline")
		ref := runs.AttemptRef{RunID: run.ID, NodeID: "impl", Attempt: 1}

		exec := newStalledExecutor(e.rnr)
		e.startOrchestrator(t, exec, 1)
		<-exec.finished
		e.expireWallDeadline(t, run.ID)

		final := lifeWaitForRunStatus(t, e.runs, run.ID, runs.RunDead)
		if final.Outcome.Code != runs.CodeWallDeadlineExceeded {
			t.Errorf("run outcome = %+v, want wall_deadline_exceeded", final.Outcome)
		}
		// The obligation still landed: the attempt ran to completion and its
		// refresh belongs in the store whatever the run's fate.
		assertCredentialConcluded(t, e, ref, 1)
		if exec.adoptionAttempted() {
			t.Error("the deadline sweep called Inspect: past the deadline it may only settle the credential, never adopt")
		}
		if n := lifeScalar[int64](t, e.pool,
			`SELECT count(*) FROM run_events WHERE run_id = $1 AND kind = $2`, run.ID, runs.EventPhaseSucceeded); n != 0 {
			t.Errorf("%d phase.succeeded events for a run that died of its deadline, want none", n)
		}
		if n := lifeScalar[int64](t, e.pool,
			`SELECT count(*) FROM artifacts WHERE run_id = $1`, run.ID); n != 0 {
			t.Errorf("%d artifact rows persisted past the deadline, want none", n)
		}
	})
}
