//go:build integration

package runs_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// publication is the bundle a publish test drives. This package has no harness
// object and does not grow one here: these are the same free helpers the rest
// of the suite uses, collected in one value because a publish test needs the
// fixture, the service, both port doubles and the ability to stop the loop
// between an approval and the attempt it releases.
type publication struct {
	f     fixture
	svc   *runs.Service
	ports fixturePorts
	ex    *fake.Executor
	run   runs.Run
	stop  func()
	// start boots a fresh loop over the same service after stop, which is how
	// a test stages a mutation the running orchestrator would have raced.
	start func()
}

func (p publication) forge() *fake.Forge         { return p.ports.forge }
func (p publication) publisher() *fake.Publisher { return p.ports.publisher }

const publishLease = 30 * time.Second

func publicationAtItsGate(t *testing.T, doc runs.DefinitionDoc) publication {
	t.Helper()
	f := newPublishFixture(t)
	svc, ports := newService(t, f, doc, runs.DefaultSnapshot())
	ex := fake.NewExecutor()
	ex.Remote(f.repoID, f.remote)
	_, stop := startStoppableOrchestrator(t, svc, ex, publishLease)
	p := publication{
		f: f, svc: svc, ports: ports, ex: ex, stop: stop,
		start: func() { startStoppableOrchestrator(t, svc, ex, publishLease) },
	}
	_, run, err := svc.CreateTask(context.Background(), principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "publish me",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForRun(t, svc, run.ID, runs.RunWaiting)
	p.run = run
	return p
}

func (p publication) approve(t *testing.T, node string) uuid.UUID {
	t.Helper()
	res, err := p.svc.DecideGate(context.Background(), principal(p.f), runs.GateDecisionInput{
		RunID: p.run.ID, NodeID: node, Decision: runs.DecisionApprove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.AuthorizationID == nil {
		t.Fatalf("approving %s must mint an authorization", node)
	}
	return *res.AuthorizationID
}

// publishIntent is the intent row as this file asserts on it. The nullable
// receipt columns are pointers here rather than the store's pgtype values:
// the rest of this suite reads its rows with raw SQL and local structs, and a
// nil pointer says "this receipt has not landed" more plainly than a Valid
// flag does.
type publishIntent struct {
	ID           uuid.UUID
	Branch       string
	TargetBranch string
	Status       string
	BaseSHA      string
	TreeHash     string
	Title        string
	Body         string
	AuthorEmail  string
	HeadSHA      *string
	PRNumber     *int64
	PRURL        *string
	HaltReason   *string
	CreatedAt    time.Time
}

func tryIntentRow(t *testing.T, f fixture, runID uuid.UUID, nodeID string) (publishIntent, bool) {
	t.Helper()
	var row publishIntent
	err := f.pool.QueryRow(context.Background(),
		`SELECT id, branch, target_branch, status, base_sha, tree_hash, title, body,
		        author_email, head_sha, pr_number, pr_url, halt_reason, created_at
		 FROM publish_intents WHERE run_id = $1 AND node_id = $2`, runID, nodeID,
	).Scan(&row.ID, &row.Branch, &row.TargetBranch, &row.Status, &row.BaseSHA, &row.TreeHash,
		&row.Title, &row.Body, &row.AuthorEmail, &row.HeadSHA, &row.PRNumber, &row.PRURL,
		&row.HaltReason, &row.CreatedAt)
	if err != nil {
		return publishIntent{}, false
	}
	return row, true
}

func intentRow(t *testing.T, f fixture, runID uuid.UUID, nodeID string) publishIntent {
	t.Helper()
	row, ok := tryIntentRow(t, f, runID, nodeID)
	if !ok {
		t.Fatalf("no publish intent for %s/%s", runID, nodeID)
	}
	return row
}

func waitForIntentStatus(t *testing.T, f fixture, runID uuid.UUID, nodeID, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	last := "<none>"
	for time.Now().Before(deadline) {
		if row, ok := tryIntentRow(t, f, runID, nodeID); ok {
			if row.Status == want {
				return
			}
			last = row.Status
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("publish intent for %s never reached %q (last %q)", nodeID, want, last)
}

func countIntents(t *testing.T, f fixture, runID uuid.UUID) int {
	t.Helper()
	return scalarCount(t, f, `SELECT count(*) FROM publish_intents WHERE run_id = $1`, runID)
}

func invalidateAuthorization(t *testing.T, f fixture, id uuid.UUID) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE approved_patches SET status = 'invalidated', settled_at = now() WHERE id = $1`, id,
	); err != nil {
		t.Fatal(err)
	}
}

// expireLease backdates a live attempt's lease so the sweep hands it to a
// second driver while the first is still inside its call.
func expireLease(t *testing.T, f fixture, runID uuid.UUID, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tag, err := f.pool.Exec(context.Background(),
			`UPDATE phase_runs SET leased_until = now() - interval '1 second'
			 WHERE run_id = $1 AND node_id = $2 AND status = 'running'`, runID, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if tag.RowsAffected() > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no running %s attempt to take the lease from", nodeID)
}

func chargeableFailures(t *testing.T, f fixture, runID uuid.UUID, nodeID string) int32 {
	t.Helper()
	var n int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT chargeable_failures FROM phase_runs
		 WHERE run_id = $1 AND node_id = $2 ORDER BY attempt DESC LIMIT 1`, runID, nodeID,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitForPushCount(t *testing.T, p *fake.Publisher, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if p.PushCount() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the publisher saw %d pushes, want %d", p.PushCount(), want)
}

func waitForRunWithin(t *testing.T, svc *runs.Service, runID uuid.UUID, want runs.RunStatus, d time.Duration) runs.Run {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		run, _, _, err := svc.GetRun(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status == want {
			return run
		}
		if run.Status.Terminal() {
			t.Fatalf("run reached %s (outcome %+v), want %s", run.Status, run.Outcome, want)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("run never reached %s", want)
	return runs.Run{}
}

// The three remote readers. They run plain git against the fixture's bare
// repository on a constructed environment, never the inherited one.
func remoteGit(t *testing.T, f fixture, args ...string) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(gitBin, args...)
	cmd.Dir = f.remote
	cmd.Env = []string{
		"PATH=" + filepath.Dir(gitBin), "HOME=" + f.remote,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func remoteBranchHead(t *testing.T, f fixture, branch string) string {
	t.Helper()
	return remoteGit(t, f, "rev-parse", "refs/heads/"+branch)
}

func remoteCommitCount(t *testing.T, f fixture, branch string) int {
	t.Helper()
	n, err := strconv.Atoi(remoteGit(t, f, "rev-list", "--count", "refs/heads/"+branch))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// pushForeignBranch puts somebody else's commit on our marker. It must be a
// DIFFERENT commit from the one this publication would build, or there is
// nothing to conflict with — so it carries its own file.
func pushForeignBranch(t *testing.T, f fixture, branch string) string {
	t.Helper()
	home := t.TempDir()
	repo := filepath.Join(home, "foreign")
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + filepath.Dir(gitBin), "HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=someone", "GIT_AUTHOR_EMAIL=someone@example.com",
		"GIT_COMMITTER_NAME=someone", "GIT_COMMITTER_EMAIL=someone@example.com",
		"GIT_AUTHOR_DATE=2026-02-02T00:00:00Z", "GIT_COMMITTER_DATE=2026-02-02T00:00:00Z",
	}
	run := func(dir string, args ...string) string {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(home, "clone", "-q", f.remote, repo)
	run(repo, "checkout", "-q", "--detach", f.baseSHA)
	if err := os.WriteFile(filepath.Join(repo, "FOREIGN.md"), []byte("not ours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "-A")
	run(repo, "commit", "-q", "-m", "foreign")
	run(repo, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	return run(repo, "rev-parse", "HEAD")
}

// assertEventOrder checks that the named kinds appear, once each, in this
// order. Event ids are commit-ordered (appendEvents holds the counter row), so
// the order is the order the receipts committed in.
func assertEventOrder(t *testing.T, svc *runs.Service, runID uuid.UUID, kinds ...string) {
	t.Helper()
	evs, err := svc.ListRunEvents(context.Background(), runID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	for _, e := range evs {
		if want[e.Kind] {
			got = append(got, e.Kind)
		}
	}
	if len(got) != len(kinds) {
		t.Fatalf("publish events = %v, want exactly %v in order", got, kinds)
	}
	for i, k := range kinds {
		if got[i] != k {
			t.Fatalf("publish events = %v, want %v: each receipt commits before the next call", got, kinds)
		}
	}
}

func TestPublish_HappyPathPushesAndOpensAPullRequest(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	authID := p.approve(t, "gate")
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 60*time.Second)

	auth := authorization(t, p.f, authID)
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Branch != runs.PublicationBranch(auth.ArtifactID) {
		t.Errorf("branch = %q, want the deterministic marker %q",
			intent.Branch, runs.PublicationBranch(auth.ArtifactID))
	}
	if intent.Status != "published" {
		t.Fatalf("intent status = %q, want published", intent.Status)
	}
	if intent.HeadSHA == nil || intent.PRNumber == nil {
		t.Fatalf("both receipts must be recorded: %+v", intent)
	}
	if auth.Status != "consumed" {
		t.Errorf("authorization status = %q, want consumed", auth.Status)
	}
	if intent.AuthorEmail != "spine@example.com" {
		t.Errorf("author_email = %q, want the requester's address snapshotted at authorize time", intent.AuthorEmail)
	}

	// The publication artifact carries what a human needs to find it.
	pub := currentArtifact(t, p.f, p.run.ID, "publish", runs.ArtifactKindPublication)
	for _, k := range []string{"branch", "head_sha", "pr_number", "pr_url", "target_branch"} {
		if pub.Payload[k] == nil {
			t.Errorf("publication artifact missing %q: %v", k, pub.Payload)
		}
	}
	// And the real remote really has the branch, at the recorded commit.
	if got := remoteBranchHead(t, p.f, intent.Branch); got != *intent.HeadSHA {
		t.Errorf("remote head = %q, want the recorded %q", got, *intent.HeadSHA)
	}
	// Ordered receipts: intent, then push, then PR.
	assertEventOrder(t, p.svc, p.run.ID,
		runs.EventPublishIntentCreated, runs.EventPublishPushed, runs.EventPublishPROpened)
}

// ExpectedHead is the whole basis of Task 10's settlement: it must rebuild the
// commit PushPatch produced, from the same request, without touching a ref.
// Nothing else in this task calls it, so without this the rebuild could drift
// from the push and every ownership check downstream would fail open.
func TestPublish_ExpectedHeadRebuildsTheCommitThatWasPushed(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.approve(t, "gate")
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 60*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	req := p.publisher().LastRequest()
	if req.Branch != intent.Branch {
		t.Fatalf("last push request was for %q, want this publication's %q", req.Branch, intent.Branch)
	}
	got, err := p.publisher().ExpectedHead(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got != *intent.HeadSHA {
		t.Fatalf("ExpectedHead = %q, want the pushed %q: the rebuild is what proves a receiptless branch is ours",
			got, *intent.HeadSHA)
	}
	if n := remoteCommitCount(t, p.f, intent.Branch); n != 2 {
		t.Errorf("the marker carries %d commits, want 2: ExpectedHead must touch no ref", n)
	}
}

func TestPublish_WithoutAnAuthorizationIsPermanentlyStale(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	invalidateAuthorization(t, p.f, authID) // as a revision would
	p.start()

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 60*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodeAuthorizationStale {
		t.Fatalf("outcome = %+v, want authorization_stale", got.Outcome)
	}
	if p.publisher().PushCount() != 0 {
		t.Fatal("a stale authorization must abort BEFORE any forge call (spec §3 Publishing)")
	}
	if _, ok := tryIntentRow(t, p.f, p.run.ID, "publish"); ok {
		t.Fatal("no intent may be created without a live authorization")
	}
}

// The claim's own predicate refuses an expired run, so a publish attempt is
// never even entered. That is a claim-layer guarantee and the test is named
// for it: ConsumeAuthorization's wall-deadline predicate is the SECOND fence
// and is pinned directly in the store package
// (TestConsumeAuthorization_RefusesAnExpiredRun), because nothing driven
// through the orchestrator can reach it.
func TestPublish_AnExpiredRunIsNeverEvenClaimed(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	p.approve(t, "gate")
	backdateWallDeadline(t, p.f, p.run.ID)
	p.start()

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 60*time.Second)
	if p.publisher().PushCount() != 0 || p.forge().OpenPRCount() != 0 {
		t.Fatal("an expired run must reach no forge and no remote")
	}
	if _, ok := tryIntentRow(t, p.f, p.run.ID, "publish"); ok {
		t.Fatal("no intent may be created for an expired run")
	}
}

// A run terminalized by a SIBLING node between the claim and the authorize
// transaction must not get a publication.
//
// ConsumeAuthorization carries the wall-deadline predicate and NO run-status
// predicate, and ClaimPhase's own `r.status = 'running'` is evaluated before
// this window opens. Without the run read at the top of authorizePublish the
// intent commits and the push lands against a FAILED run — and
// ListExpiredRunIDs matches only 'pending', 'running' and 'waiting', so that
// branch is an orphan nothing ever reconciles.
//
// The window is entered through the run row's own lock, which is the only
// rendezvous that is not a race: ClaimPhase locks `FOR UPDATE OF pr` and
// merely READS runs, so a held transaction that has already flipped the status
// does not block the claim — while authorizePublish's GetRunForUpdate blocks
// on it until the flip commits. Taking the lock BEFORE the row becomes
// claimable is what makes the ordering total.
func TestPublish_ARunTerminalizedBetweenTheClaimAndTheAuthorizeGetsNoPublication(t *testing.T) {
	ctx := context.Background()
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	p.approve(t, "gate")

	// Materialize the publish attempt ourselves, parked, so the loop can start
	// without claiming it. materializeReady will not insert a second live row
	// for a node that already has one.
	mustExec(t, p.f,
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, chargeable_failures,
		                         status, generation, not_before, created_at)
		 VALUES ($1, 'publish', 1, 3, 0, 'pending', 1, now() + interval '1 hour', now())`, p.run.ID)
	p.start()

	// Hold the run row and flip it, uncommitted.
	tx, err := p.f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM runs WHERE id = $1 FOR UPDATE`, p.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE runs SET status = 'failed', finished_at = now() WHERE id = $1`, p.run.ID); err != nil {
		t.Fatal(err)
	}

	// Now let a worker claim it. The claim reads the committed 'running'.
	mustExec(t, p.f,
		`UPDATE phase_runs SET not_before = now() - interval '1 second'
		 WHERE run_id = $1 AND node_id = 'publish'`, p.run.ID)
	waitForPhaseStatus(t, p.f, p.run.ID, "publish", "running", 30*time.Second)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The attempt discovers the dead run and requeues UNCHARGED. Waiting for
	// it to stop RUNNING rather than for its successor is what makes the
	// assertions below the ones that fire: without the guard this attempt
	// succeeds instead, and there is no successor to wait for.
	waitForPhaseLeavesRunning(t, p.f, p.run.ID, "publish", 30*time.Second)
	if _, ok := tryIntentRow(t, p.f, p.run.ID, "publish"); ok {
		t.Fatal("a publication began against a run that is not running: nothing will ever reconcile its branch")
	}
	if p.publisher().PushCount() != 0 {
		t.Fatal("nothing may reach the remote once the run is terminal")
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0: the publication never began, so it spent nothing", got)
	}
}

// waitForPhaseLeavesRunning returns once the node's newest attempt row is
// anything but 'running' — settled, or requeued behind a successor.
func runStatus(t *testing.T, f fixture, runID uuid.UUID) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status FROM runs WHERE id = $1`, runID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func waitForPhaseLeavesRunning(t *testing.T, f fixture, runID uuid.UUID, nodeID string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var got string
		if err := f.pool.QueryRow(context.Background(),
			`SELECT status FROM phase_runs WHERE run_id = $1 AND node_id = $2
			 ORDER BY attempt DESC LIMIT 1`, runID, nodeID).Scan(&got); err == nil && got != "running" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never left running", nodeID)
}

func waitForPhaseStatus(t *testing.T, f fixture, runID uuid.UUID, nodeID, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var got string
		if err := f.pool.QueryRow(context.Background(),
			`SELECT status FROM phase_runs WHERE run_id = $1 AND node_id = $2
			 ORDER BY attempt DESC LIMIT 1`, runID, nodeID).Scan(&got); err == nil && got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never reached %q", nodeID, want)
}

func TestPublish_CrashAfterPushAdoptsTheBranchInsteadOfPushingAgain(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.publisher().CrashAfterPush(true) // the push lands, the process dies before the receipt
	startBackdater(t, p.f, p.run.ID)   // publish_resumable parks 15s; the wait is real time we refuse to spend
	p.approve(t, "gate")

	// The intent is INSERTED 'pending', so waiting for that status proves
	// nothing about the push. Wait for the effect itself.
	waitForPushCount(t, p.publisher(), 1, 30*time.Second)
	if got := intentRow(t, p.f, p.run.ID, "publish").Status; got != "pending" {
		t.Fatalf("intent status = %q, want pending: the push landed and its receipt did not", got)
	}

	// Cross a second boundary before the retry. The commit stamp gitops
	// writes is RFC3339, which has second resolution: a retry inside the same
	// second rebuilds the same sha even from the wall clock, so without this
	// the test would pass with Date unpinned — and the whole adopt path rests
	// on the date being the intent's, not the clock's.
	time.Sleep(1100 * time.Millisecond)
	p.publisher().CrashAfterPush(false)
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 90*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if got := remoteCommitCount(t, p.f, intent.Branch); got != 2 {
		t.Fatalf("remote has %d commits on the marker, want 2 (seed + one publication)", got)
	}
	if got := p.publisher().CreatedCount(); got != 1 {
		t.Errorf("branch creations = %d, want 1: the retry must ADOPT the branch, not replace it", got)
	}
	// publish.adopted is emitted for an adopted BRANCH here and for an adopted
	// pull request in openPublication; this run adopts only the branch, so one
	// is the right count — the payload's "branch"/"pr_number" keys are what
	// tell the two apart when both can happen.
	if n := countEvents(t, p.svc, p.run.ID, runs.EventPublishAdopted); n != 1 {
		t.Errorf("publish.adopted events = %d, want 1", n)
	}
}

// The commit's author identity is snapshotted on the intent, and this is why.
//
// A commit sha hashes the author identity, and recovery proves a receiptless
// branch is ours by rebuilding the SAME sha. The requester's address lives on
// a mutable users row and the orchestrator caches it per process, so an
// address that changed between two attempts of one publication — a restart is
// enough to re-resolve it — would rebuild a DIFFERENT commit. gitops.Publish
// would then find the marker at a sha it cannot claim and halt the
// publication for a human, with the branch already pushed and correct.
func TestPublish_AChangedRequesterAddressStillAdoptsTheBranch(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.publisher().CrashAfterPush(true)
	p.approve(t, "gate")
	waitForPushCount(t, p.publisher(), 1, 30*time.Second)

	// A restart with a cold plan cache AND a moved address: either alone
	// leaves a cached value in place.
	p.stop()
	mustExec(t, p.f, `UPDATE users SET email = 'renamed@example.com' WHERE id = $1`, p.f.userID)
	p.publisher().CrashAfterPush(false)
	startBackdater(t, p.f, p.run.ID)
	p.start()

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 90*time.Second)
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.AuthorEmail != "spine@example.com" {
		t.Errorf("author_email = %q, want the address the intent snapshotted", intent.AuthorEmail)
	}
	if got := remoteCommitCount(t, p.f, intent.Branch); got != 2 {
		t.Fatalf("remote has %d commits on the marker, want 2: the retry rebuilt a different commit", got)
	}
	if n := countEvents(t, p.svc, p.run.ID, runs.EventPublishAdopted); n != 1 {
		t.Errorf("publish.adopted events = %d, want 1: the rebuilt commit must match and be adopted", n)
	}
}

func TestPublish_CrashAfterPRCreateAdoptsTheExistingPullRequest(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.forge().CrashAfterPR(true)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")
	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pushed", 45*time.Second)

	p.forge().CrashAfterPR(false)
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 90*time.Second)

	if got := p.forge().OpenPRCount(); got != 1 {
		t.Fatalf("the forge opened %d pull requests, want exactly 1: a lost receipt must be FOUND, not recreated", got)
	}
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.PRNumber == nil || *intent.PRNumber == 0 {
		t.Fatal("the adopted PR's number must be recorded")
	}
}

func TestPublish_ForeignBranchOnOurMarkerHaltsForAHuman(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	auth := authorization(t, p.f, authID)
	foreign := pushForeignBranch(t, p.f, runs.PublicationBranch(auth.ArtifactID))
	p.start()

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 60*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict", got.Outcome)
	}
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil || *intent.HaltReason == "" {
		t.Fatalf("the intent must halt with a reason a human can act on: %+v", intent)
	}
	if p.forge().OpenPRCount() != 0 {
		t.Error("a halted publication opens nothing")
	}
	if head := remoteBranchHead(t, p.f, intent.Branch); head != foreign {
		t.Errorf("the marker is at %q, want the foreign %q left untouched", head, foreign)
	}
	// And it stays halted: this is not a retry class.
	time.Sleep(500 * time.Millisecond)
	if intentRow(t, p.f, p.run.ID, "publish").Status != "halted" {
		t.Error("a halted intent must never be resumed automatically")
	}
}

// A marker that already carries a pull request nobody can prove is ours halts
// for a human, and opens nothing.
//
// This is the leg that makes the FIND before the create load-bearing. Without
// it the machine would open a second request on a marker that already has one
// — and a forge that refuses duplicates would hide the mistake behind a
// conflict, so "no duplicate opened" is not the property being protected here;
// "we looked, and we could not prove it was ours" is.
func TestPublish_AForeignPullRequestOnOurMarkerHaltsForAHuman(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	auth := authorization(t, p.f, authID)
	// A request from our marker to a target nobody approved: definitively not
	// this publication's, and impossible to adopt.
	p.forge().SeedPullRequest(runs.PublicationBranch(auth.ArtifactID), runs.PullRequestRef{
		Number: 99, URL: "https://fake.forge/spine/repo/pull/99",
		HeadSHA: "0000000000000000000000000000000000000000", Base: "release", State: "open",
	})
	p.start()

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 60*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict", got.Outcome)
	}
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil || *intent.HaltReason == "" {
		t.Fatalf("the intent must halt with a reason a human can act on: %+v", intent)
	}
	if got := p.forge().CreateAttemptCount(); got != 0 {
		t.Errorf("the forge saw %d create attempts, want 0: the find comes first, unconditionally, "+
			"and a marker we cannot claim is a human's call before anything is tried", got)
	}
	if got := p.forge().PullRequestCount(intent.Branch); got != 1 {
		t.Errorf("pull requests on %s = %d, want the one stranger's left alone", intent.Branch, got)
	}
	// The branch itself was pushed before the pull request was attempted, and
	// it stays: the halt is about the request, not the commit.
	if intent.HeadSHA == nil {
		t.Error("the push receipt must survive a halt at the pull-request step")
	}
}

func TestPublish_TransientForgeFailureResumesWithoutRePushing(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.forge().FailPRTimes(2, runs.CodeForgeUnavailable)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 120*time.Second)

	if got := p.publisher().PushCount(); got != 1 {
		t.Errorf("pushes = %d, want 1: a retried publication resumes from 'pushed', it does not re-push", got)
	}
	if got := p.forge().OpenPRCount(); got != 1 {
		t.Errorf("PRs opened = %d, want 1", got)
	}
	if got := countIntents(t, p.f, p.run.ID); got != 1 {
		t.Errorf("intents = %d, want 1: one per (run, node), resumed across attempts", got)
	}
	// The assertion unchargedWhileUnfinished exists for: two forge outages
	// spent none of the node's failure budget. Charged, three would exhaust it
	// and terminalize a run whose branch is already on the forge.
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0: an outage must not exhaust an unfinished publication", got)
	}
}

// A publish attempt that loses its lease is reconciled through the publish
// machine, not through Executor.Inspect.
//
// reconcile_lost is CHARGEABLE, and max_attempts is 3: routed there, three lost
// leases exhaust the node, terminalize the run, supersede every phase and leave
// a branch on the forge that nothing will ever reconcile. Three is therefore
// the number this test takes — two would pass either way.
func TestPublish_LostLeasesDoNotExhaustAPublication(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().HangBeforePush()
	t.Cleanup(release)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")

	for i := 0; i < 3; i++ {
		expireLease(t, p.f, p.run.ID, "publish")
		waitForPhaseAttempt(t, p.f, p.run.ID, "publish", int32(i+2), 45*time.Second)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Fatalf("chargeable failures = %d, want 0: a lost publish lease charges nothing", got)
	}
	if p.publisher().PushCount() != 0 {
		t.Fatal("nothing reached the remote while the publisher was wedged")
	}

	release()
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 90*time.Second)
	// The marker carries the seed plus exactly one publication commit. The
	// count is asserted rather than the publisher's creation counter because
	// releasing the hold frees every fenced driver at once: each rebuilds the
	// SAME commit (the date is pinned to the intent), and git reports a push
	// of a ref that already carries that commit as success — so more than one
	// call can believe it created the branch while the remote holds one.
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if got := remoteCommitCount(t, p.f, intent.Branch); got != 2 {
		t.Errorf("remote has %d commits on the marker, want 2 (seed + one publication)", got)
	}
	if got := countIntents(t, p.f, p.run.ID); got != 1 {
		t.Errorf("intents = %d, want 1", got)
	}
}

func waitForPhaseAttempt(t *testing.T, f fixture, runID uuid.UUID, nodeID string, want int32, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var got int32
		err := f.pool.QueryRow(context.Background(),
			`SELECT max(attempt) FROM phase_runs WHERE run_id = $1 AND node_id = $2`, runID, nodeID,
		).Scan(&got)
		if err == nil && got >= want {
			return
		}
		if st := runStatus(t, f, runID); st == "failed" || st == "dead" {
			t.Fatalf("the run died (%s) before %s reached attempt %d: its failures were charged", st, nodeID, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never reached attempt %d", nodeID, want)
}

// Two drivers, one intent — the state a lease takeover creates and cannot
// undo, because it can fence a worker's database writes but not a request
// already on the wire.
//
// The interleaving that matters: A creates the pull request and is held BEFORE
// its receipt commits, so the intent is still 'pushed'. B's pre-create probe
// sees nothing (the forge has not listed it yet) and its create is refused
// with "already exists". A maybeHalt there would win its CAS and halt a
// publication that succeeded — telling a human to reconcile a pull request
// that is open, correct, and exactly what was approved.
func TestPublish_ALostCreatePRRaceAdoptsTheWinnerRatherThanHalting(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.forge().HoldFirstPullRequest()
	t.Cleanup(release)
	p.forge().HideListingsAfterCreate(1)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")

	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pushed", 45*time.Second)
	waitForPullRequests(t, p.forge(), 1, 30*time.Second) // A is now held before its receipt

	// Take the lease away and let a second driver run the same intent.
	expireLease(t, p.f, p.run.ID, "publish")
	waitForIntentStatus(t, p.f, p.run.ID, "publish", "published", 60*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status == "halted" {
		t.Fatalf("the losing driver halted a successful publication: %+v", intent)
	}
	if intent.PRNumber == nil {
		t.Fatalf("both drivers must converge on the one real publication: %+v", intent)
	}
	if got := p.forge().PullRequestCount(intent.Branch); got != 1 {
		t.Errorf("pull requests on %s = %d, want 1: the loser must adopt, never open a second",
			intent.Branch, got)
	}
	release()
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 90*time.Second)
}

func waitForPullRequests(t *testing.T, f *fake.Forge, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f.OpenPRCount() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the forge accepted %d creates, want %d", f.OpenPRCount(), want)
}

// Every coordinate the approval pinned, refused one at a time.
//
// revalidateAuthorization is ~60 lines of argued-for refusals and the rest of
// this suite would not notice if its body were `return nil`: the stale-
// authorization test never enters it, and nothing else moves a repo, a base or
// an artifact between approval and publication. One case per coordinate is
// what makes the function falsifiable at all.
func TestPublish_RevalidationRefusesEveryCoordinateTheApprovalPinned(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, f fixture, runID uuid.UUID)
		wantMsg string
	}{
		{
			name: "the repository was renamed",
			mutate: func(t *testing.T, f fixture, runID uuid.UUID) {
				mustExec(t, f, `UPDATE repos SET slug = 'other/repo' WHERE id = $1`, f.repoID)
			},
			wantMsg: "it moved after the approval",
		},
		{
			name: "the default branch moved",
			mutate: func(t *testing.T, f fixture, runID uuid.UUID) {
				mustExec(t, f, `UPDATE repos SET default_branch = 'trunk' WHERE id = $1`, f.repoID)
			},
			wantMsg: "the repository's default branch is now",
		},
		{
			name: "the run re-pinned its base",
			mutate: func(t *testing.T, f fixture, runID uuid.UUID) {
				mustExec(t, f, `UPDATE runs SET base_sha = 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeef' WHERE id = $1`, runID)
			},
			wantMsg: "the approval pinned base",
		},
		{
			name: "the approved artifact's tree hash changed under the approval",
			mutate: func(t *testing.T, f fixture, runID uuid.UUID) {
				mustExec(t, f,
					`UPDATE artifacts SET payload = jsonb_set(payload, '{tree_hash}', '"deadbeef"')
					 WHERE run_id = $1 AND node_id = 'impl' AND type = 'patch'`, runID)
			},
			wantMsg: "the approved artifact's tree hash is",
		},
		{
			name: "the approval came from a gate this definition does not have",
			mutate: func(t *testing.T, f fixture, runID uuid.UUID) {
				mustExec(t, f, `UPDATE approved_patches SET gate_node_id = 'nope' WHERE run_id = $1`, runID)
			},
			wantMsg: "does not authorize",
		},
		{
			name: "the publish node consumes a different patch than the one approved",
			mutate: func(t *testing.T, f fixture, runID uuid.UUID) {
				// A newer patch for the same node in the same generation: what
				// a second implement attempt would leave behind. The publish
				// node would consume it, and it is not what was approved.
				mustExec(t, f,
					`INSERT INTO artifacts (id, run_id, node_id, attempt, generation, type, payload, parent_ids, created_at)
					 SELECT uuidv7(), run_id, node_id, attempt + 1, generation, type, payload, '[]'::jsonb, now()
					 FROM artifacts WHERE run_id = $1 AND node_id = 'impl' AND type = 'patch'`, runID)
			},
			wantMsg: "but the approval authorized",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := publicationAtItsGate(t, gatedConveyorDoc())
			p.stop()
			p.approve(t, "gate")
			tc.mutate(t, p.f, p.run.ID)
			p.start()

			got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 60*time.Second)
			if got.Outcome == nil || got.Outcome.Code != runs.CodeAuthorizationStale {
				t.Fatalf("outcome = %+v, want authorization_stale", got.Outcome)
			}
			if !strings.Contains(got.Outcome.Message, tc.wantMsg) {
				t.Errorf("message = %q, want it to name %q", got.Outcome.Message, tc.wantMsg)
			}
			if p.publisher().PushCount() != 0 {
				t.Error("a stale coordinate must abort before anything reaches the remote")
			}
			if _, ok := tryIntentRow(t, p.f, p.run.ID, "publish"); ok {
				t.Error("a refused revalidation must create no intent")
			}
		})
	}
}

func mustExec(t *testing.T, f fixture, sql string, args ...any) {
	t.Helper()
	tag, err := f.pool.Exec(context.Background(), sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() == 0 {
		t.Fatalf("%q changed nothing; the case would prove nothing", sql)
	}
}
