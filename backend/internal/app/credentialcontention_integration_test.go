//go:build integration

package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/testdb"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
	"github.com/giiorg/caesar/backend/internal/runs/runner"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// contentionAuthFile is a minimal Codex auth.json: the credentials service
// refuses an agent_auth_file it cannot read a refresh token out of.
const contentionAuthFile = `{"tokens":{"id_token":"id","access_token":"at","refresh_token":"rt-contention","account_id":"acct"}}`

// leasingExecutor is the runner's credential gate without the daemon: it
// takes the single-flight lease through the PRODUCTION appCredentialLeaser
// adapter, reports contention as the same zero-charge credential_busy wait
// runner.Execute returns, holds the lease for the duration of the phase, and
// releases at the end.
//
// The Docker runner cannot drive this test — it wants a daemon, an agent
// image and a real repo — but neither of those decides contention. What does
// is the adapter's ErrLeaseBusy translation against the real assets
// aggregate, and the spine's handling of the wait; both are the real thing
// here. The runner's own busy-to-outcome mapping is pinned separately, in
// runner's lease_test.go.
type leasingExecutor struct {
	inner  runs.Executor
	leaser appCredentialLeaser
	hold   time.Duration

	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	busy        int
	acquired    int
}

func (e *leasingExecutor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	if req.Credential.Kind != string(assets.CredentialAgentAuthFile) {
		return e.inner.Execute(ctx, req)
	}
	lease, secret, err := e.leaser.Acquire(ctx, req.Credential.ID, req.Ref.String())
	if errors.Is(err, runner.ErrLeaseBusy) {
		e.count(&e.busy)
		return runs.ExecutionResult{}, runs.OutcomeError{Outcome: runs.Outcome{
			Class: runs.OutcomeTransient, Code: runs.CodeCredentialBusy,
			Message: fmt.Sprintf("credential %s lease held by another attempt", req.Credential.ID),
		}}
	}
	if err != nil {
		return runs.ExecutionResult{}, err
	}
	defer func() {
		_ = e.leaser.Release(context.WithoutCancel(ctx), req.Credential.ID, req.Ref.String(), lease.Epoch)
	}()
	if string(secret) != contentionAuthFile {
		return runs.ExecutionResult{}, fmt.Errorf("acquire returned %q, want the stored auth file", secret)
	}

	e.enter()
	defer e.leave()
	// Hold long enough that a second holder would overlap observably rather
	// than by luck: without the lease, two workers would run concurrently.
	select {
	case <-ctx.Done():
		return runs.ExecutionResult{}, ctx.Err()
	case <-time.After(e.hold):
	}
	return e.inner.Execute(ctx, req)
}

func (e *leasingExecutor) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	return e.inner.Inspect(ctx, ref)
}

func (e *leasingExecutor) enter() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.acquired++
	e.inFlight++
	if e.inFlight > e.maxInFlight {
		e.maxInFlight = e.inFlight
	}
}

func (e *leasingExecutor) leave() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inFlight--
}

func (e *leasingExecutor) count(n *int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	*n++
}

func (e *leasingExecutor) stats() (busy, acquired, maxInFlight int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy, e.acquired, e.maxInFlight
}

// The contention test (spec §6): two runs share one agent_auth_file, a third
// party holds its lease, and both runs wait. Waiting must be free — no
// execution budget, no charged failures, a bounded number of attempt rows —
// and once the lease is free the two runs go through one at a time.
func TestCredentialContention_RunsWaitFreeThenRunSerially(t *testing.T) {
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
	forge := fake.NewForge(map[uuid.UUID]string{})
	runsSvc := runs.NewService(pool, clk, rec,
		runsDefinitionSource{svc: workflowsSvc}, runsAssetSource{svc: assetsSvc}, runsAssetSource{svc: assetsSvc},
		forge, fake.NewPublisher(nil), principalSource{svc: identitySvc},
		runs.Params{Snapshot: runs.DefaultSnapshot()})

	project, err := assetsSvc.CreateProject(ctx, admin, "contention", "")
	if err != nil {
		t.Fatal(err)
	}
	authCred, err := assetsSvc.CreateCredential(ctx, admin, "codex-subscription",
		assets.CredentialAgentAuthFile, []byte(contentionAuthFile))
	if err != nil {
		t.Fatal(err)
	}
	forgeCred, err := assetsSvc.CreateCredential(ctx, admin, "github-bot", assets.CredentialForgeToken, []byte("ghp-x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assetsSvc.RegisterHarness(ctx, admin, assets.NewHarness{
		Name: "cx", Kind: assets.HarnessCodex, ImageRef: "ghcr.io/x/codex:dev",
		ProviderEndpoint: "https://chatgpt.com/backend-api", CredentialID: authCred.ID,
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := assetsSvc.CreateRepo(ctx, admin, project.ID, assets.NewRepo{
		Forge: assets.ForgeGitHub, Slug: "contention/demo", DefaultBranch: "main", CredentialID: forgeCred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	forge.Head(repo.ID, "0123456789012345678901234567890123456789")

	wf, err := workflowsSvc.CreateWorkflow(ctx, admin, "contention", "")
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

	// A third party takes the credential first: whoever holds it, the two
	// runs below must wait rather than fail.
	holder, _, err := assetsSvc.AcquireCredentialLease(ctx, authCred.ID, "outside-holder")
	if err != nil {
		t.Fatal(err)
	}

	exec := &leasingExecutor{
		inner: fake.NewExecutor(), leaser: appCredentialLeaser{svc: assetsSvc},
		hold: 150 * time.Millisecond,
	}
	orch := runs.NewOrchestrator(runsSvc, exec, runs.AlwaysAdmit{}, nil, runs.OrchestratorConfig{
		Workers: 2, PollInterval: 20 * time.Millisecond, LeaseTTL: 30 * time.Second,
		HeartbeatInterval: 10 * time.Second, OutboxRetention: 168 * time.Hour,
	}, discard())
	orchCtx, orchCancel := context.WithCancel(ctx)
	orchDone := make(chan struct{})
	go func() { defer close(orchDone); _ = orch.Run(orchCtx) }()
	t.Cleanup(func() { orchCancel(); <-orchDone })

	var runIDs []uuid.UUID
	for i := range 2 {
		_, run, err := runsSvc.CreateTask(ctx, admin, runs.NewTask{
			RepoID: repo.ID, WorkflowID: wf.ID, WorkflowVersion: version.Version,
			Title: fmt.Sprintf("contender %d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, run.ID)
	}

	// Both runs park behind the held lease. The wait is real time nobody
	// spends: the parked row is invisible until its not_before passes.
	for _, id := range runIDs {
		waitForParked(t, pool, id)
	}
	for _, id := range runIDs {
		if used := scalar[int64](t, pool, `SELECT execution_used_ms FROM runs WHERE id = $1`, id); used != 0 {
			t.Errorf("run %s spent %dms of execution budget while waiting, want 0", id, used)
		}
		if charged := scalar[int32](t, pool,
			`SELECT max(chargeable_failures) FROM phase_runs WHERE run_id = $1`, id); charged != 0 {
			t.Errorf("run %s took %d charged failures while waiting, want 0", id, charged)
		}
		// One wait costs exactly one new row: the failed attempt and its
		// single parked successor, no more.
		if n := scalar[int64](t, pool,
			`SELECT count(*) FROM phase_runs WHERE run_id = $1 AND node_id = 'impl'`, id); n != 2 {
			t.Errorf("run %s created %d attempt rows for one wait, want 2", id, n)
		}
	}
	if busy, acquired, _ := exec.stats(); busy < 2 || acquired != 0 {
		t.Fatalf("busy=%d acquired=%d, want both runs turned away and neither admitted", busy, acquired)
	}

	// The holder concludes; the waits are the only thing that was ever in
	// the way. Backdating stands in for the pinned 30s floor — the wait is
	// real time this test refuses to spend, not behaviour under test here.
	if err := assetsSvc.ReleaseCredentialLease(ctx, authCred.ID, "outside-holder", holder.Epoch); err != nil {
		t.Fatal(err)
	}
	stopBackdating := startBackdatingParked(t, pool)
	for _, id := range runIDs {
		waitForRunStatus(t, runsSvc, id, runs.RunSucceeded)
	}
	stopBackdating()

	busy, acquired, maxInFlight := exec.stats()
	if acquired != 2 {
		t.Errorf("%d phases held the credential, want exactly 2 — one per run", acquired)
	}
	if maxInFlight != 1 {
		t.Errorf("%d phases held the credential at once, want 1: the lease is single-flight", maxInFlight)
	}
	if busy < 2 {
		t.Errorf("only %d attempts were turned away, want at least the two initial waits", busy)
	}
	// Waiting stayed free to the end: only the work that actually ran is
	// charged, and no wait ever counted as a failure.
	for _, id := range runIDs {
		if charged := scalar[int32](t, pool,
			`SELECT max(chargeable_failures) FROM phase_runs WHERE run_id = $1`, id); charged != 0 {
			t.Errorf("run %s ended with %d charged failures, want 0", id, charged)
		}
	}
}

// waitForParked polls for the run's parked credential_busy successor.
func waitForParked(t *testing.T, pool *pgxpool.Pool, runID uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n := scalar[int64](t, pool,
			`SELECT count(*) FROM phase_runs
			 WHERE run_id = $1 AND status = 'pending' AND not_before IS NOT NULL`, runID)
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s never parked behind the held credential", runID)
}

// startBackdatingParked keeps pulling parked rows into the past so a test
// can walk several waits without sleeping through the pinned backoff.
func startBackdatingParked(t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_, _ = pool.Exec(ctx, `UPDATE phase_runs SET not_before = now() - interval '1 second'
					WHERE status = 'pending' AND not_before > now()`)
			}
		}
	}()
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

func waitForRunStatus(t *testing.T, svc *runs.Service, runID uuid.UUID, want runs.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		run, _, _, err := svc.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == want {
			return
		}
		if run.Status == runs.RunFailed || run.Status == runs.RunSucceeded {
			t.Fatalf("run %s reached %s, want %s (outcome %+v)", runID, run.Status, want, run.Outcome)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s never reached %s", runID, want)
}

// scalar reads one value, failing the test on any error.
func scalar[T any](t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}
