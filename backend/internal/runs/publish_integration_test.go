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
	"unicode/utf8"

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

// derefOr renders a nullable receipt column for a failure message. A bare %v
// on the pointer prints an address, which says nothing about what went wrong.
func derefOr(s *string, fallback string) string {
	if s == nil {
		return fallback
	}
	return *s
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

// The AMBIGUOUS halt, which is a different branch of openPublication from the
// one above and reached only by a candidate that can be neither confirmed nor
// ruled out — here, a request on our marker with no base recorded.
//
// It is a separate test because it is a separate call site. The two halts read
// alike from outside, so a single test proves nothing about the other one; that
// showed up as a live gap when the halt's outcome code became per-site, and a
// wrong code at this branch went uncaught.
func TestPublish_AnUnprovableRequestOnOurMarkerHaltsForAHuman(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	auth := authorization(t, p.f, authID)
	// No base: absent evidence must not become permission. matchPublication
	// cannot rule it in or out, so nobody here may choose.
	p.forge().SeedPullRequest(runs.PublicationBranch(auth.ArtifactID), runs.PullRequestRef{
		Number: 41, URL: "https://fake.forge/spine/repo/pull/41",
		HeadSHA: "0000000000000000000000000000000000000000", Base: "", State: "open",
	})
	p.start()

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 60*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict: a marker we cannot read is a conflict, "+
			"not a dead credential and not a missing repository", got.Outcome)
	}
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil ||
		!strings.Contains(*intent.HaltReason, "none is provably this publication's") {
		t.Fatalf("the intent must halt naming the ambiguity a human has to resolve: %+v", intent)
	}
	if got := p.forge().CreateAttemptCount(); got != 0 {
		t.Errorf("the forge saw %d create attempts, want 0: nothing is tried against a marker "+
			"whose contents we cannot read", got)
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
	// One probe, from the one attempt that ran the push step. PushCount above
	// says the remote was written once; only this says the two retries skipped
	// the whole step rather than re-entering it and finding nothing to do.
	if got := p.forge().BranchProbeCount(); got != 1 {
		t.Errorf("branch probes = %d, want 1: a resumed publication re-enters the pull-request step, not the push step", got)
	}
}

// The push leg of unchargedWhileUnfinished, which nothing else can reach.
//
// Every other publisher injection is already uncharged before it arrives —
// HangBeforePush yields publish_not_started, CrashAfterPush yields
// publish_resumable — so only a CHARGEABLE transient out of the push asks
// whether the downgrade happens at that call site. push_rejected is that
// outcome: max_attempts is 3, so charged, two rejections plus one more failure
// exhaust the node and terminalize a run whose branch may already be pushed.
func TestPublish_ARejectedPushDoesNotExhaustTheNode(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.publisher().FailPushTimes(2, runs.CodePushRejected)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 120*time.Second)

	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0: a rejected push must not spend the budget of a "+
			"publication that is still unfinished", got)
	}
	if got := p.publisher().PushCount(); got != 1 {
		t.Errorf("pushes that reached the remote = %d, want 1: the two refusals landed nothing", got)
	}
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if got := remoteCommitCount(t, p.f, intent.Branch); got != 2 {
		t.Errorf("remote has %d commits on the marker, want 2 (seed + one publication)", got)
	}
}

// Crash window 8: a receipt transaction that fails at COMMIT, after its effect
// has landed. Both receipts, one test each, driven by making the receipt
// statement itself fail — the fault seam, not a mutation of the code under
// test.
//
// Without receiptFailure at the call site the raw error default-denies to a
// PERMANENT internal_error, evaluate terminalizes the run, terminalizeRun
// supersedes every live phase, and nothing revisits a failed run — so the
// branch, and the pull request, are orphans.
func TestPublish_AFailedPushReceiptResumesRatherThanKillingTheRun(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.f.faults.Fail("RecordPushReceipt", 0, 1)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 120*time.Second)
	if n := p.f.faults.Remaining("RecordPushReceipt"); n != 0 {
		t.Fatalf("%d armed receipt failures never fired; this test proved nothing", n)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0", got)
	}
	// The push really happened before its receipt failed, so the retry must
	// ADOPT the branch rather than build a second commit on it.
	if got := p.publisher().CreatedCount(); got != 1 {
		t.Errorf("branch creations = %d, want 1", got)
	}
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "published" || intent.HeadSHA == nil || intent.PRNumber == nil {
		t.Fatalf("the publication must complete on the retry: %+v", intent)
	}
	if got := remoteCommitCount(t, p.f, intent.Branch); got != 2 {
		t.Errorf("remote has %d commits on the marker, want 2", got)
	}
}

func TestPublish_AFailedPullRequestReceiptResumesRatherThanKillingTheRun(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.f.faults.Fail("RecordPRReceipt", 0, 1)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 120*time.Second)
	if n := p.f.faults.Remaining("RecordPRReceipt"); n != 0 {
		t.Fatalf("%d armed receipt failures never fired; this test proved nothing", n)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0", got)
	}
	// The request was open before the receipt failed: the retry's unconditional
	// find is what turns a lost receipt into a found one instead of a second
	// pull request on one approval.
	if got := p.forge().OpenPRCount(); got != 1 {
		t.Errorf("the forge opened %d pull requests, want 1", got)
	}
	if got := p.publisher().PushCount(); got != 1 {
		t.Errorf("pushes = %d, want 1: the retry resumes from 'pushed'", got)
	}
}

// The halt receipt is the third one, and the worst placed: three of
// haltPublication's four callers stand on a 'pushed' intent, so a permanent
// answer here kills the run with the branch live on the forge AND the halt
// unrecorded — nothing tells the human there is anything to reconcile.
//
// Re-running the halt is convergent, which is what makes resuming honest, and
// this test walks that: the first halt transaction fails, the second lands.
func TestPublish_AFailedHaltReceiptResumesRatherThanKillingTheRun(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	auth := authorization(t, p.f, authID)
	foreign := pushForeignBranch(t, p.f, runs.PublicationBranch(auth.ArtifactID))
	p.f.faults.Fail("HaltPublishIntent", 0, 1)
	startBackdater(t, p.f, p.run.ID)
	p.start()

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 120*time.Second)
	if n := p.f.faults.Remaining("HaltPublishIntent"); n != 0 {
		t.Fatalf("%d armed halt failures never fired; this test proved nothing", n)
	}
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict: a halt that could not commit is a database "+
			"blip on a live intent, not a verdict about the publication", got.Outcome)
	}
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil || *intent.HaltReason == "" {
		t.Fatalf("the retry must land the halt a human acts on: %+v", intent)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0", got)
	}
	if head := remoteBranchHead(t, p.f, intent.Branch); head != foreign {
		t.Errorf("the marker is at %q, want the foreign %q left untouched", head, foreign)
	}
}

// The artifact read inside pushPublication runs with the intent already
// created and its authorization already CONSUMED. A permanent answer there
// kills a publication that nothing can re-authorize, over a failed SELECT.
//
// skip = 1 because two places read this artifact: revalidateAuthorization,
// inside the authorize transaction, and then the push. Only the second is
// standing on a live intent, and only the second is this finding.
func TestPublish_AFailedArtifactReadResumesRatherThanKillingTheRun(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	p.approve(t, "gate")
	p.f.faults.Fail("GetArtifactByID", 1, 1)
	startBackdater(t, p.f, p.run.ID)
	p.start()

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 120*time.Second)
	if n := p.f.faults.Remaining("GetArtifactByID"); n != 0 {
		t.Fatalf("%d armed read failures never fired; this test proved nothing", n)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0", got)
	}
	// The failure was before the effect, so exactly one push and one intent.
	if got := p.publisher().PushCount(); got != 1 {
		t.Errorf("pushes = %d, want 1: the failed read happened before anything reached the remote", got)
	}
	if got := countIntents(t, p.f, p.run.ID); got != 1 {
		t.Errorf("intents = %d, want 1: the retry resumes the intent, it does not re-authorize", got)
	}
}

// The run's EXECUTION budget is a second door into terminalization, and it is
// not the node's failure budget: unchargedWhileUnfinished closes the one and
// says nothing about the other. evaluateFailure pre-checks the execution
// budget on every retryable failure and demotes a breach to a permanent
// budget_exhausted — so a run that reaches its publish node nearly spent would
// die there with its branch on the forge, and ListExpiredRunIDs never revisits
// a failed run.
func TestPublish_ASpentExecutionBudgetDoesNotTerminalizeAnUnfinishedPublication(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().HangBeforePush()
	t.Cleanup(release)
	p.forge().FailPRTimes(1, runs.CodeForgeUnavailable)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")

	// Spend the run down to its last millisecond WHILE the attempt is wedged.
	// The claim already read the old figures, so this attempt's own deadline is
	// untouched — only evaluateFailure's budget pre-check sees the change,
	// which is the door being tested and nothing else.
	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pending", 45*time.Second)
	mustExec(t, p.f,
		`UPDATE runs SET execution_used_ms = execution_budget_ms - 1 WHERE id = $1`, p.run.ID)
	release()

	// The branch is on the forge and the forge then refused the request. That
	// failure is uncharged, so it must requeue — waitForPhaseAttempt fails
	// loudly if the run died instead, which is exactly the regression.
	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pushed", 45*time.Second)
	waitForPhaseAttempt(t, p.f, p.run.ID, "publish", 2, 60*time.Second)

	if got := runStatus(t, p.f, p.run.ID); got != "running" {
		t.Fatalf("run status = %q, want running: a budget breach must not terminalize a publication "+
			"whose branch is already pushed", got)
	}
	if got := intentRow(t, p.f, p.run.ID, "publish").Status; got != "pushed" {
		t.Errorf("intent status = %q, want pushed: the publication is still live", got)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0", got)
	}
}

// The WALL DEADLINE is the third door into a terminalized publication, and the
// only one that needs nothing to be exhausted: evaluateFailure refuses to park
// a retry whose wake falls past the run's deadline and terminalizes instead.
// publish_resumable backs off ~15 seconds, so a single forge hiccup that late
// in a run kills it 'failed' with the branch already pushed — and at that
// moment wall_deadline is still in the FUTURE, so ListExpiredRunIDs (pending,
// running, waiting) never matches the run again and nothing ever settles the
// branch. It is cheaper to reach than the budget door: no near-spent run
// needed, only a forge that hiccups near the end.
//
// Parking is the safe answer because ClaimPhase carries wall_deadline > now():
// the parked row is unclaimable either way, so the exemption buys a run that
// reaches the expiry sweep as 'running' at the cost of nothing that could have
// run.
//
// The deadline is pulled in BETWEEN two attempts rather than during one: the
// claim reads the deadline into the attempt, so only an attempt claimed after
// the update carries it. That is why the second attempt is the one under test —
// and it re-enters at the pull-request step, so the window it needs is a
// fraction of a second, not a clone and a push.
func TestPublish_ADeadlineInsideTheBackoffParksRatherThanTerminalizing(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.forge().FailPRTimes(2, runs.CodeForgeUnavailable)
	p.approve(t, "gate")

	// Attempt 1, against a deadline a day out: the branch lands, the request is
	// refused, and the successor parks ~15 seconds ahead.
	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pushed", 45*time.Second)
	waitForPhaseAttempt(t, p.f, p.run.ID, "publish", 2, 45*time.Second)

	// Now put the deadline INSIDE publish_resumable's backoff and wake the
	// successor by hand. No backdater: the ordering here is this test's, not a
	// goroutine's, so the attempt under test is claimed after the update and
	// cannot be claimed before it.
	mustExec(t, p.f,
		`UPDATE runs SET wall_deadline = now() + interval '12 seconds' WHERE id = $1`, p.run.ID)
	mustExec(t, p.f,
		`UPDATE phase_runs SET not_before = now() - interval '1 second'
		 WHERE run_id = $1 AND node_id = 'publish' AND status = 'pending'`, p.run.ID)

	// Attempt 2 fails the same way with the near deadline in hand. A third
	// attempt row is the whole assertion: waitForPhaseAttempt fails loudly if
	// the run was terminalized instead, which is the regression.
	waitForPhaseAttempt(t, p.f, p.run.ID, "publish", 3, 45*time.Second)

	if got := runStatus(t, p.f, p.run.ID); got != "running" {
		t.Fatalf("run status = %q, want running: a wait that outlasts the deadline must not "+
			"terminalize a publication whose branch is already on the forge", got)
	}
	// The successor really did park past the deadline — an unclaimable row is
	// exactly the trade, and without this the case could pass on a wait that
	// fitted inside the deadline and exercised nothing.
	var parkedPast bool
	if err := p.f.pool.QueryRow(context.Background(),
		`SELECT coalesce(p.not_before > r.wall_deadline, false)
		 FROM phase_runs p JOIN runs r ON r.id = p.run_id
		 WHERE p.run_id = $1 AND p.node_id = 'publish' AND p.attempt = 3`, p.run.ID,
	).Scan(&parkedPast); err != nil {
		t.Fatal(err)
	}
	if !parkedPast {
		t.Errorf("the successor wakes before the wall deadline: this case did not reach the refusal it is about")
	}
	if got := intentRow(t, p.f, p.run.ID, "publish").Status; got != "pushed" {
		t.Errorf("intent status = %q, want pushed: the publication is still live", got)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0", got)
	}
}

// Crash window 10, the last one the plan enumerated: the AUTHORIZE transaction
// failing at COMMIT. Nothing has reached the forge yet, which is exactly why
// this is dangerous rather than harmless — notStartedUnlessTyped is the only
// thing between nine untyped statement errors in that transaction and a
// permanent internal_error that terminalizes a run which has caused nothing at
// all, over a database blip.
//
// Both statements that write are driven: the one that spends the approval and
// the one that creates the intent. Each rolls its transaction back, so the
// retry must re-authorize from an untouched approval and publish exactly once.
func TestPublish_AFailedAuthorizationTransactionDoesNotKillTheRun(t *testing.T) {
	for _, stmt := range []string{"ConsumeAuthorization", "CreatePublishIntent"} {
		t.Run(stmt, func(t *testing.T) {
			p := publicationAtItsGate(t, gatedConveyorDoc())
			p.stop()
			p.approve(t, "gate")
			p.f.faults.Fail(stmt, 0, 1)
			p.start()

			waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 120*time.Second)
			if n := p.f.faults.Remaining(stmt); n != 0 {
				t.Fatalf("%d armed %s failures never fired; this test proved nothing", n, stmt)
			}
			if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
				t.Errorf("chargeable failures = %d, want 0: nothing had begun, so nothing was spent", got)
			}
			// The rollback is what makes the retry honest: one intent from one
			// approval, and one publication on the remote.
			if got := countIntents(t, p.f, p.run.ID); got != 1 {
				t.Errorf("intents = %d, want 1: the rolled-back transaction left no half-authorization", got)
			}
			if got := p.publisher().PushCount(); got != 1 {
				t.Errorf("pushes = %d, want 1", got)
			}
			intent := intentRow(t, p.f, p.run.ID, "publish")
			if intent.Status != "published" || intent.PRNumber == nil {
				t.Fatalf("the publication must complete on the retry: %+v", intent)
			}
		})
	}
}

// A halted intent is the one status runPublish must refuse on ARRIVAL. It is
// reached when the driver that halted was fenced — a lease takeover drops its
// permanent outcome — and its successor picks the settled row up.
//
// Without the refusal the successor walks past both step blocks (neither
// 'pending' nor 'pushed' matches) and returns SUCCESS carrying a publication
// artifact with no head sha and no pull request in it.
func TestPublish_AHaltedIntentRefusesEveryLaterAttempt(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().HangBeforePush()
	t.Cleanup(release)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")

	// Wedge the first driver with the intent created and nothing pushed, then
	// halt the intent under it, as its fenced predecessor would have.
	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pending", 45*time.Second)
	mustExec(t, p.f,
		`UPDATE publish_intents SET status = 'halted', halt_reason = 'a human is reconciling this'
		 WHERE run_id = $1 AND node_id = 'publish'`, p.run.ID)
	probes := p.forge().BranchProbeCount()
	expireLease(t, p.f, p.run.ID, "publish")

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 90*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict", got.Outcome)
	}
	if !strings.Contains(got.Outcome.Message, "a human is reconciling this") {
		t.Errorf("message = %q, want the halt reason carried to the run", got.Outcome.Message)
	}
	if after := p.forge().BranchProbeCount(); after != probes {
		t.Errorf("branch probes went %d -> %d: a halted publication is re-probed by nobody", probes, after)
	}
	if got := p.publisher().PushCount(); got != 0 {
		t.Errorf("pushes = %d, want 0: nothing may reach the remote for a halted intent", got)
	}
	if n := scalarCount(t, p.f,
		`SELECT count(*) FROM artifacts WHERE run_id = $1 AND node_id = 'publish'`, p.run.ID); n != 0 {
		t.Errorf("a halted publication produced %d artifacts, want 0: success here would record a "+
			"publication with no head sha and no pull request in it", n)
	}
}

// A forge answer no retry can change, arriving with the branch ALREADY on the
// forge, halts for a human instead of failing the run.
//
// This is the hole a real forge adapter opens. Before it, every permanent
// answer reaching this step was a publish_conflict, which halted. A revoked
// token and a deleted repository are permanent too, and left alone they would
// terminalize the run — which supersedes every phase, is revisited by no sweep,
// and leaves the pushed marker branch with nobody looking for it. Halting is
// the mechanism the design already has for "a person must look at this", and
// this is exactly that.
//
// The refusal is unbounded rather than scripted-n-times, so the assertion that
// the machine asked ONCE is a real property and not an artifact of the script.
//
// BOTH proven-permanent causes are driven, and each asserts its OWN outcome
// code. That is what makes this the falsifier for the whole halt set rather
// than for one member of it: a version that recognised only auth_expired, and a
// version that stamped one code on every halt, each fail a leg. The code is the
// machine-readable field a UI, an alert or a metric keys on; the halt reason is
// prose, and prose is not a field anything can match on.
func TestPublish_APermanentForgeRefusalHaltsRatherThanStrandingThePushedBranch(t *testing.T) {
	causes := map[string]string{
		"a revoked credential":                 runs.CodeAuthExpired,
		"a repository the forge will not show": runs.CodeRepoUnavailable,
	}
	for name, code := range causes {
		t.Run(name, func(t *testing.T) {
			p := publicationAtItsGate(t, gatedConveyorDoc())
			p.forge().FailPRPermanently(code)
			p.approve(t, "gate")

			got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 60*time.Second)
			if got.Outcome == nil || got.Outcome.Code != code {
				t.Fatalf("outcome = %+v, want %s: the cause of a halt is what an operator "+
					"greps for, and a halt that relabels it hides which one happened", got.Outcome, code)
			}
			intent := intentRow(t, p.f, p.run.ID, "publish")
			if intent.Status != "halted" || intent.HaltReason == nil || *intent.HaltReason == "" {
				t.Fatalf("the intent must halt with a reason a human can act on: %+v", intent)
			}
			if !strings.Contains(*intent.HaltReason, "permanent pull-request refusal") {
				t.Errorf("halt reason = %q; it must name what the forge actually said", *intent.HaltReason)
			}
			// The branch IS pushed. That is the whole reason this must halt
			// rather than fail: a failed run strands it.
			if intent.HeadSHA == nil {
				t.Fatal("the push receipt must survive a halt at the pull-request step")
			}
			if head := remoteBranchHead(t, p.f, intent.Branch); head != *intent.HeadSHA {
				t.Errorf("the marker is at %q, want the pushed %q", head, *intent.HeadSHA)
			}
			// And the machine stops asking: an answer no retry can change is
			// asked once.
			if n := p.forge().CreateAttemptCount(); n != 1 {
				t.Errorf("create attempts = %d, want 1", n)
			}
			// A halt is not a retry class, and nothing resumes it.
			time.Sleep(500 * time.Millisecond)
			if intentRow(t, p.f, p.run.ID, "publish").Status != "halted" {
				t.Error("a halted intent must never be resumed automatically")
			}
		})
	}
}

// The same permanent answer, arriving at the two PROBES instead of at the
// create — and the probes are where it arrives FIRST, because both of them run
// before the effect they guard.
//
// pushPublication's first act is the recovery probe, before the artifact read
// and before the push; openPublication's is the pull-request listing, before
// any create. So a credential revoked mid-run is refused at a probe, never at
// the create, and a probe that discarded the adapter's verdict would leave the
// halt set unreachable for the one cause it was extended for. The publication
// would park as a transient instead and re-probe until the wall deadline: no
// halt, no halt_reason, no publish.halted event, and a person told nothing.
//
// Each leg asserts its own probe COUNT, and that is the assertion the outcome
// cannot make: a machine that retries a permanent refusal reaches the same
// intent row eventually — via settlement, at the deadline — so only "asked
// once" separates halting from spinning.
func TestPublish_APermanentForgeRefusalAtAProbeHaltsRatherThanRetrying(t *testing.T) {
	t.Run("the recovery probe, with nothing pushed yet", func(t *testing.T) {
		p := publicationAtItsGate(t, gatedConveyorDoc())
		p.forge().FailBranchProbesPermanently(runs.CodeAuthExpired)
		p.approve(t, "gate")

		got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 45*time.Second)
		if got.Outcome == nil || got.Outcome.Code != runs.CodeAuthExpired {
			t.Fatalf("outcome = %+v, want auth_expired: a credential the forge rejected is "+
				"proven permanent, and relabelling it transient retries a dead token", got.Outcome)
		}
		intent := intentRow(t, p.f, p.run.ID, "publish")
		if intent.Status != "halted" || intent.HaltReason == nil || *intent.HaltReason == "" {
			t.Fatalf("the intent must halt with a reason a human can act on: %+v", intent)
		}
		if intent.HeadSHA != nil {
			t.Errorf("head_sha = %q; the probe refused before the push, so no receipt exists", *intent.HeadSHA)
		}
		if n := countEvents(t, p.svc, p.run.ID, runs.EventPublishHalted); n != 1 {
			t.Errorf("publish.halted events = %d, want 1: the halt is what tells a person to look", n)
		}
		if n := p.forge().BranchProbeCount(); n != 1 {
			t.Errorf("branch probes = %d, want 1: an answer no retry can change is asked once", n)
		}
	})

	t.Run("the pull-request listing, on a branch already pushed", func(t *testing.T) {
		p := publicationAtItsGate(t, gatedConveyorDoc())
		p.forge().FailFindPermanently(runs.CodeAuthExpired)
		p.approve(t, "gate")

		got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 45*time.Second)
		if got.Outcome == nil || got.Outcome.Code != runs.CodeAuthExpired {
			t.Fatalf("outcome = %+v, want auth_expired", got.Outcome)
		}
		intent := intentRow(t, p.f, p.run.ID, "publish")
		if intent.Status != "halted" || intent.HaltReason == nil || *intent.HaltReason == "" {
			t.Fatalf("the intent must halt with a reason a human can act on: %+v", intent)
		}
		// The branch IS pushed, which is the whole reason this halts rather
		// than fails: a failed run strands it.
		if intent.HeadSHA == nil {
			t.Fatal("the push receipt must survive a halt at the pull-request step")
		}
		if head := remoteBranchHead(t, p.f, intent.Branch); head != *intent.HeadSHA {
			t.Errorf("the marker is at %q, want the pushed %q", head, *intent.HeadSHA)
		}
		if n := p.forge().FindAttemptCount(); n != 1 {
			t.Errorf("listings = %d, want 1: an answer no retry can change is asked once", n)
		}
		if n := p.forge().OpenPRCount(); n != 0 {
			t.Errorf("pull requests opened = %d, want 0: the listing never said the marker was clear", n)
		}
	})
}

// adoptConflictingPublication's THIRD answer: the forge refused a second
// request and then did not list the first. Read-after-write lag, or a replica
// behind the write — guessing either way is wrong, so it resumes, uncharged,
// on a branch that is already pushed.
//
// HideListingsAfterCreate(2) is what reaches it: one hidden listing is the
// second driver's pre-create probe (which is what makes its create conflict at
// all), and the second is the re-query that would otherwise have adopted.
func TestPublish_AForgeThatDoesNotYetListItsOwnPullRequestResumes(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.forge().HoldFirstPullRequest()
	t.Cleanup(release)
	p.forge().HideListingsAfterCreate(2)
	startBackdater(t, p.f, p.run.ID)
	p.approve(t, "gate")

	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pushed", 45*time.Second)
	waitForPullRequests(t, p.forge(), 1, 30*time.Second) // the winner is held before its receipt

	expireLease(t, p.f, p.run.ID, "publish")
	waitForIntentStatus(t, p.f, p.run.ID, "publish", "published", 90*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status == "halted" {
		t.Fatalf("a forge that had not listed its own request is not a human's problem: %+v", intent)
	}
	if intent.PRNumber == nil || *intent.PRNumber != 1 {
		t.Fatalf("the intent must record the ONE real request the forge accepted: %+v", intent)
	}
	if got := p.forge().PullRequestCount(intent.Branch); got != 1 {
		t.Errorf("pull requests on %s = %d, want 1", intent.Branch, got)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0: the forge's own lag must not spend a budget", got)
	}
	release()
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 90*time.Second)
}

// adoptConflictingPublication's SECOND answer: the marker really does carry a
// stranger's request, and the pre-create probe simply did not see it.
//
// The halt reason is the assertion that matters. Both halt legs end the same
// way — failed run, halted intent — and only the message says which one
// decided; a leg folded into the other still reaches a halt, one probe later.
func TestPublish_AStrangersPullRequestSeenOnlyAfterTheCreateConflictHalts(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	auth := authorization(t, p.f, authID)
	branch := runs.PublicationBranch(auth.ArtifactID)
	p.forge().SeedPullRequest(branch, runs.PullRequestRef{
		Number: 99, URL: "https://fake.forge/spine/repo/pull/99",
		HeadSHA: "0000000000000000000000000000000000000000", Base: "release", State: "open",
	})
	// Hide it from the pre-create probe only. Without this the find before the
	// create halts first and this leg is never entered.
	p.forge().HideNextListings(1)
	p.start()

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 90*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict", got.Outcome)
	}
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.HaltReason == nil || !strings.Contains(*intent.HaltReason, "forge refused a second pull request") {
		t.Fatalf("halt reason = %q, want the one adoptConflictingPublication writes: the "+
			"pre-create check's message means this leg was never entered", derefOr(intent.HaltReason, "<none>"))
	}
	if intent.Status != "halted" {
		t.Errorf("intent status = %q, want halted", intent.Status)
	}
	if got := p.forge().CreateAttemptCount(); got != 1 {
		t.Errorf("create attempts = %d, want 1: one blind create, then a human", got)
	}
	if got := p.forge().PullRequestCount(branch); got != 1 {
		t.Errorf("pull requests on %s = %d, want the stranger's left alone", branch, got)
	}
	if intent.HeadSHA == nil {
		t.Error("the push receipt must survive a halt at the pull-request step")
	}
}

// A title longer than the forge cap is truncated, and truncated on a rune
// boundary. The task's title is the only unbounded thing that reaches a forge,
// and a request refused for size would surface as an unexplained permanent
// failure with the authorization already spent.
//
// The title is set after the gate and read by a cold plan cache, because the
// tasks row is where a title long enough to test this can come from: the
// intake path caps it well below the publish cap.
func TestPublish_AnOversizedTitleIsTruncatedOnARuneBoundary(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	// Three-byte runes, so the cap lands INSIDE one: 250 is not a multiple of
	// 3, and a naive cut would emit a half rune.
	long := strings.Repeat("日", 100)
	mustExec(t, p.f,
		`UPDATE tasks SET title = $2 WHERE id = (SELECT task_id FROM runs WHERE id = $1)`, p.run.ID, long)
	p.approve(t, "gate")
	p.start()

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 60*time.Second)
	intent := intentRow(t, p.f, p.run.ID, "publish")
	if len(intent.Title) != 249 {
		t.Fatalf("title is %d bytes, want 249 — the 250-byte cap cut back to the last whole rune", len(intent.Title))
	}
	if !utf8.ValidString(intent.Title) {
		t.Error("the truncated title is not valid UTF-8")
	}
	if !strings.HasPrefix(long, intent.Title) {
		t.Error("the truncated title must be a prefix of the task's")
	}
}

// A requester whose address has gone missing is our own data being wrong, and
// it repeats. Committing as `<>` would publish an unattributable commit under
// a human's approval, which is worse than refusing.
func TestPublish_ARequesterWithNoAddressRefusesToPublish(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	p.approve(t, "gate")
	mustExec(t, p.f, `UPDATE users SET email = '' WHERE id = $1`, p.f.userID)
	p.start()

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 60*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodeInternalError {
		t.Fatalf("outcome = %+v, want internal_error: an empty address is not a transient blip, "+
			"and retrying it would spin until the wall deadline", got.Outcome)
	}
	if !strings.Contains(got.Outcome.Message, "has no address") {
		t.Errorf("message = %q, want it to name the missing address", got.Outcome.Message)
	}
	if _, ok := tryIntentRow(t, p.f, p.run.ID, "publish"); ok {
		t.Error("no intent may be created for a publication that cannot be attributed")
	}
	if p.publisher().PushCount() != 0 {
		t.Error("nothing may reach the remote")
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
			// The id leg, on its own. Every other coordinate still agrees, so
			// nothing but `auth.RepoID != cp.repo.ID` can refuse this — which
			// is the point: an approval carrying a different repository ROW is
			// a different repository even when its slug reads the same.
			name: "the approval was granted against a different repository row",
			mutate: func(t *testing.T, f fixture, runID uuid.UUID) {
				var other uuid.UUID
				if err := f.pool.QueryRow(context.Background(),
					`INSERT INTO repos (project_id, forge_kind, slug, default_branch, credential_id)
					 VALUES ($1, 'github', 'spine/other', 'main', $2) RETURNING id`,
					f.projectID, f.credID,
				).Scan(&other); err != nil {
					t.Fatal(err)
				}
				mustExec(t, f, `UPDATE approved_patches SET repo_id = $2 WHERE run_id = $1`, runID, other)
			},
			wantMsg: "but the run targets",
		},
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
