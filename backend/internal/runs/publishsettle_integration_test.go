//go:build integration

package runs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// This file drives the two sweeps that meet a half-finished publication: the
// LEASE sweep, which reconciles one against its intent instead of asking the
// Executor, and the WALL-DEADLINE sweep, which may resume nothing and so can
// only ask the forge what already exists and write the answer down.
//
// It uses the same free helpers the rest of the suite does (publish_integration_test.go
// says why this package grows no harness object) plus the few readers below,
// which are the ones that genuinely did not exist.

// noCredentialExecutor is an Executor that is NOT a CredentialReconciler —
// production's shape (serve.go wires NotImplementedExecutor, and a publish node
// bypasses the executor entirely anyway).
//
// It exists because the fake Executor implements the reconciler half, so every
// other test in this package runs with o.credentials != nil. Fencing used to
// live behind that field, which meant production fenced nothing at all.
type noCredentialExecutor struct{ ex *fake.Executor }

func (e noCredentialExecutor) Execute(ctx context.Context, req runs.ExecutionRequest) (runs.ExecutionResult, error) {
	return e.ex.Execute(ctx, req)
}

func (e noCredentialExecutor) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	return e.ex.Inspect(ctx, ref)
}

// phaseOutcome reads one attempt's recorded outcome. The suite could read a
// phase's status and its charge counter but never the typed answer itself,
// which is the only thing that tells two uncharged transients apart.
func phaseOutcome(t *testing.T, f fixture, runID uuid.UUID, nodeID string, attempt int32) (class, code, message string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT coalesce(outcome->>'class',''), coalesce(outcome->>'code',''), coalesce(outcome->>'message','')
		 FROM phase_runs WHERE run_id = $1 AND node_id = $2 AND attempt = $3`, runID, nodeID, attempt,
	).Scan(&class, &code, &message); err != nil {
		t.Fatalf("read %s#%d outcome: %v", nodeID, attempt, err)
	}
	return class, code, message
}

// maxPhaseAttempt is how far a node's attempt ordinal got. It is the assertion
// that separates "this answer was final" from "this answer was retried": a
// permanent outcome inserts no successor, and an uncharged transient does.
func maxPhaseAttempt(t *testing.T, f fixture, runID uuid.UUID, nodeID string) int32 {
	t.Helper()
	var n int32
	if err := f.pool.QueryRow(context.Background(),
		`SELECT coalesce(max(attempt), 0) FROM phase_runs WHERE run_id = $1 AND node_id = $2`, runID, nodeID,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// strandRunningPublishAttempt is the crash a lease sweep meets: a publish
// attempt still 'running' whose worker is gone and whose lease has expired.
//
// strandedAttempt (reconcileadoption_integration_test.go) cannot serve — it
// hardcodes node_id='plan' and inserts 'reconciling', so it enters through the
// stuck sweep rather than the lease sweep this path is about.
func strandRunningPublishAttempt(t *testing.T, f fixture, runID uuid.UUID) uuid.UUID {
	t.Helper()
	var phaseID uuid.UUID
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO phase_runs (run_id, node_id, attempt, max_attempts, chargeable_failures, status,
		                         worker_id, claimed_at, leased_until, generation, created_at)
		 VALUES ($1, 'publish', 1, 3, 0, 'running', 'dead-worker',
		         now() - interval '5 seconds', now() - interval '1 second', 1, now())
		 RETURNING id`, runID,
	).Scan(&phaseID); err != nil {
		t.Fatal(err)
	}
	return phaseID
}

// stagePublishIntent writes the durable position a dead worker left behind,
// deriving every pinned coordinate from the authorization the gate minted so
// the row is the one the real path would have written.
func stagePublishIntent(t *testing.T, f fixture, authID uuid.UUID, status string,
	headSHA *string, prNumber *int64, prURL, haltReason *string,
) {
	t.Helper()
	mustExec(t, f,
		`INSERT INTO publish_intents (run_id, node_id, attempt, authorization_id, repo_id, forge_kind,
		                              repo_slug, credential_id, branch, target_branch, base_sha,
		                              tree_hash, title, body, author_email, commit_message, status,
		                              head_sha, pr_number, pr_url, halt_reason, created_at, updated_at)
		 SELECT ap.run_id, ap.publish_node_id, 1, ap.id, ap.repo_id, ap.forge_kind, ap.repo_slug,
		        r.credential_id, 'caesar/' || ap.artifact_id, ap.target_branch, ap.base_sha,
		        ap.tree_hash, 'staged', 'staged', 'spine@example.com', 'staged commit', $2,
		        $3, $4, $5, $6, now(), now()
		 FROM approved_patches ap JOIN repos r ON r.id = ap.repo_id
		 WHERE ap.id = $1`,
		authID, status, headSHA, prNumber, prURL, haltReason)
}

func strptr(s string) *string { return &s }

// startAbsenceBackdater ages absent_since so a test does not spend the whole
// 30-second settle grace in real time.
//
// It never SETS the stamp, which is what keeps the guard falsifiable: settlement
// still has to take the first sighting itself, and its first halt attempt still
// has to match nothing. All this changes is how long the second probe's
// comparison waits — exactly what startBackdater does for a parked retry.
func startAbsenceBackdater(t *testing.T, f fixture, runID uuid.UUID) {
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
				_, _ = f.pool.Exec(ctx,
					`UPDATE publish_intents SET absent_since = now() - interval '1 hour'
					 WHERE run_id = $1 AND absent_since IS NOT NULL
					   AND absent_since > now() - interval '1 hour'`, runID)
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
}

func backdateWallDeadlineBy(t *testing.T, f fixture, runID uuid.UUID, interval string) {
	t.Helper()
	mustExec(t, f,
		`UPDATE runs SET wall_deadline = now() - $2::interval WHERE id = $1`, runID, interval)
}

func waitForForgeBranchProbe(t *testing.T, fg *fake.Forge, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fg.BranchProbeCount() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the forge saw %d branch probes, want %d: settlement never looked", fg.BranchProbeCount(), want)
}

func waitForForgePRProbe(t *testing.T, fg *fake.Forge, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fg.PullRequestProbeCount() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the forge saw %d pull-request probes, want %d: settlement never looked",
		fg.PullRequestProbeCount(), want)
}

// ---------------------------------------------------------------------------
// The lease sweep: reconciliation reads the intent, not the Executor.
// ---------------------------------------------------------------------------

// A publication whose receipts are both durable is ADOPTED when its worker dies
// before it could evaluate. The alternative is a second publication for one
// approval — or, through Executor.Inspect, a chargeable reconcile_lost for an
// attempt that in fact succeeded.
//
// The attempt ordinal is the assertion that makes this falsifiable: 'published'
// falling through to publish_resumable also ends in a succeeded run (runPublish
// refuses to re-drive a published intent), and only a successor row says the
// answer was "come back later" rather than "this is done".
func TestReconcile_APublishedPublicationIsAdoptedRatherThanRepublished(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	head := "1111111111111111111111111111111111111111"
	number := int64(7)
	stagePublishIntent(t, p.f, authID, "published",
		&head, &number, strptr("https://fake.forge/spine/repo/pull/7"), nil)
	strandRunningPublishAttempt(t, p.f, p.run.ID)
	p.start()

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 60*time.Second)

	if got := maxPhaseAttempt(t, p.f, p.run.ID, "publish"); got != 1 {
		t.Errorf("publish reached attempt %d, want 1: an adopted publication is finished, not retried", got)
	}
	if got := p.publisher().PushCount(); got != 0 {
		t.Errorf("pushes = %d, want 0: reconciliation adopts, it does not re-publish", got)
	}
	if got := p.forge().OpenPRCount(); got != 0 {
		t.Errorf("pull requests opened = %d, want 0", got)
	}
	if got := p.forge().BranchProbeCount(); got != 0 {
		t.Errorf("branch probes = %d, want 0: a published intent answers from the row alone", got)
	}
	pub := currentArtifact(t, p.f, p.run.ID, "publish", runs.ArtifactKindPublication)
	if got := pub.Payload["head_sha"]; got != head {
		t.Errorf("adopted artifact head_sha = %v, want the recorded %q", got, head)
	}
	if got := pub.Payload["pr_number"]; got != float64(number) {
		t.Errorf("adopted artifact pr_number = %v, want the recorded %d", got, number)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0", got)
	}
	if n := countEvents(t, p.svc, p.run.ID, runs.EventPhaseReconciling); n != 1 {
		t.Errorf("phase.reconciling events = %d, want 1", n)
	}
}

// A worker that died before creating the intent left nothing behind. The next
// attempt authorizes from scratch, and it must be told the attempt was NOT
// lost: reconcile_lost is chargeable, and this publication never began.
//
// The OUTCOME CODE is the assertion, not the charge counter. Task 9 already
// routes a lost publish lease to the uncharged publish_resumable, so a test
// that only checks chargeable_failures passes without this task existing at
// all. The two codes differ in exactly one observable way — publish_not_started
// requeues immediately ({0,0,0}, retry.go) where publish_resumable floors at
// five seconds — so the successor's park is asserted too.
func TestReconcile_APublicationThatNeverStartedIsNotChargedAsLost(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	p.approve(t, "gate")
	strandRunningPublishAttempt(t, p.f, p.run.ID) // running, lease expired, no intent
	p.start()

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunSucceeded, 90*time.Second)

	class, code, _ := phaseOutcome(t, p.f, p.run.ID, "publish", 1)
	if class != string(runs.OutcomeTransient) || code != runs.CodePublishNotStarted {
		t.Errorf("outcome = %s/%s, want transient/%s: nothing had begun, so nothing was lost",
			class, code, runs.CodePublishNotStarted)
	}
	if got := chargeableFailures(t, p.f, p.run.ID, "publish"); got != 0 {
		t.Errorf("chargeable failures = %d, want 0", got)
	}
	var parkSeconds float64
	if err := p.f.pool.QueryRow(context.Background(),
		`SELECT extract(epoch FROM (next.not_before - prev.finished_at))
		 FROM phase_runs prev, phase_runs next
		 WHERE prev.run_id = $1 AND prev.node_id = 'publish' AND prev.attempt = 1
		   AND next.run_id = $1 AND next.node_id = 'publish' AND next.attempt = 2`, p.run.ID,
	).Scan(&parkSeconds); err != nil {
		t.Fatal(err)
	}
	if parkSeconds > 2 {
		t.Errorf("the successor parks %.1fs out, want an immediate requeue: publish_resumable's "+
			"five-second floor would park a publication that never began", parkSeconds)
	}
	if got := p.publisher().PushCount(); got != 1 {
		t.Errorf("pushes = %d, want 1: the successor authorizes and publishes normally", got)
	}
}

// A halted publication is settled, and reconciliation must say so rather than
// re-driving it. The run dies naming what a human has to reconcile.
//
// Again the attempt ordinal is what makes it falsifiable: fall through to
// publish_resumable and the successor's runPublish reaches the same halted
// outcome one attempt later, so only "no successor was created" separates a
// permanent answer here from a retry that happened to end the same way.
func TestReconcile_AHaltedPublicationIsPermanentAndProbesNothing(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	stagePublishIntent(t, p.f, authID, "halted", nil, nil, nil,
		strptr("a human is reconciling this"))
	strandRunningPublishAttempt(t, p.f, p.run.ID)
	p.start()

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 60*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict", got.Outcome)
	}
	if !strings.Contains(got.Outcome.Message, "a human is reconciling this") {
		t.Errorf("message = %q, want the halt reason carried to the run", got.Outcome.Message)
	}
	if n := maxPhaseAttempt(t, p.f, p.run.ID, "publish"); n != 1 {
		t.Errorf("publish reached attempt %d, want 1: nothing resumes a halt but a human", n)
	}
	if got := p.forge().BranchProbeCount(); got != 0 {
		t.Errorf("branch probes = %d, want 0: a halted publication is re-probed by nobody", got)
	}
	if got := p.publisher().PushCount(); got != 0 {
		t.Errorf("pushes = %d, want 0", got)
	}
	if n := scalarCount(t, p.f,
		`SELECT count(*) FROM artifacts WHERE run_id = $1 AND node_id = 'publish'`, p.run.ID); n != 0 {
		t.Errorf("a halted publication produced %d artifacts, want 0", n)
	}
}

// haltPublication's lost CAS stops being a theoretical leg once settlement
// exists: an expired run's settlement halts intents too, so a live driver can
// now reach its own halt and find the row already settled by somebody else.
//
// Treating that as a successful halt is the bug. The driver would report ITS
// reason — a stale view of a conflict it did not resolve — as the run's
// outcome, and write a second publish.halted event for a decision that was
// already made and recorded once.
//
// The other writer is a plain UPDATE here rather than the settlement path, and
// deliberately so: settlement only runs past the wall deadline, and fencing an
// expired run's phase cancels the very driver this test needs alive. What is
// under test is the driver's side of the race, which is identical whoever won.
func TestPublish_AHaltRacedByAnotherWriterAdoptsTheSettledRow(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	auth := authorization(t, p.f, authID)
	foreign := pushForeignBranch(t, p.f, runs.PublicationBranch(auth.ArtifactID))
	release := p.publisher().HangBeforePush()
	t.Cleanup(release)
	p.start()

	// The driver is wedged with a live intent and a marker it cannot claim.
	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pending", 45*time.Second)
	mustExec(t, p.f,
		`UPDATE publish_intents SET status = 'halted', halt_reason = 'settled by somebody else'
		 WHERE run_id = $1 AND node_id = 'publish'`, p.run.ID)
	release() // it now pushes, is refused, and goes to halt a row already halted

	got := waitForRunWithin(t, p.svc, p.run.ID, runs.RunFailed, 90*time.Second)
	if got.Outcome == nil || got.Outcome.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v, want publish_conflict", got.Outcome)
	}
	if !strings.Contains(got.Outcome.Message, "settled by somebody else") {
		t.Errorf("outcome message = %q, want the reason the row already carries: a driver that "+
			"lost the halt CAS must adopt the settled row, not report its own stale view",
			got.Outcome.Message)
	}
	if n := countEvents(t, p.svc, p.run.ID, runs.EventPublishHalted); n != 0 {
		t.Errorf("publish.halted events = %d, want 0: the halt was already made and recorded "+
			"once; the losing transaction must commit nothing", n)
	}
	if head := remoteBranchHead(t, p.f, intentRow(t, p.f, p.run.ID, "publish").Branch); head != foreign {
		t.Errorf("the marker is at %q, want the foreign %q left untouched", head, foreign)
	}
}

// ---------------------------------------------------------------------------
// The wall-deadline sweep: settlement asks, and never causes.
// ---------------------------------------------------------------------------

// The push lands; the deadline expires before its receipt commits. The run must
// still die — the deadline is not negotiable — but it must not die pretending
// nothing was pushed, because nothing revisits a dead run.
func TestExpiredRun_APushThatLandedIsRecordedBeforeTheRunDies(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().HangAfterPush()
	t.Cleanup(release)
	p.approve(t, "gate")

	waitForPushCount(t, p.publisher(), 1, 45*time.Second)
	if got := intentRow(t, p.f, p.run.ID, "publish").Status; got != "pending" {
		t.Fatalf("intent status = %q, want pending: the push landed and its receipt did not", got)
	}
	backdateWallDeadline(t, p.f, p.run.ID)
	startAbsenceBackdater(t, p.f, p.run.ID)

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 90*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.HeadSHA == nil {
		t.Fatalf("a branch exists on the remote and the record does not mention it: %+v", intent)
	}
	if got := remoteBranchHead(t, p.f, intent.Branch); got != *intent.HeadSHA {
		t.Errorf("recorded head %q does not match the remote's %q", *intent.HeadSHA, got)
	}
	if intent.Status != "halted" {
		t.Errorf("intent status = %q: an expired run's intent must reach a settled state, "+
			"not stay in-flight forever", intent.Status)
	}
	if got := p.publisher().CreatedCount(); got != 1 {
		t.Errorf("branch creations = %d, want 1: settlement asks, it never pushes", got)
	}
}

// The ownership half of the same path. A branch on our marker that this
// authorization did not build is a human's problem, and recording its head as
// ours would write a stranger's commit into the run's record — permanently,
// since nothing revisits a dead run.
func TestExpiredRun_ABranchWeCannotProveIsOursIsHaltedNotRecorded(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	authID := p.approve(t, "gate")
	auth := authorization(t, p.f, authID)
	foreign := pushForeignBranch(t, p.f, runs.PublicationBranch(auth.ArtifactID))
	// Wedged before the push, so the live path never reaches its own conflict
	// check: the answer under test is settlement's rebuilt-sha comparison.
	release := p.publisher().HangBeforePush()
	t.Cleanup(release)
	p.start()

	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pending", 45*time.Second)
	backdateWallDeadline(t, p.f, p.run.ID)

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 90*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "halted" {
		t.Fatalf("intent status = %q, want halted: %+v", intent.Status, intent)
	}
	if intent.HaltReason == nil || !strings.Contains(*intent.HaltReason, "did not produce") {
		t.Fatalf("halt reason = %q, want the ownership refusal: recording a head we cannot "+
			"prove is ours is the failure this comparison exists for",
			derefOr(intent.HaltReason, "<none>"))
	}
	if intent.HeadSHA != nil {
		t.Errorf("head_sha = %q, want none: a branch we could not claim was recorded as ours", *intent.HeadSHA)
	}
	if head := remoteBranchHead(t, p.f, intent.Branch); head != foreign {
		t.Errorf("the marker is at %q, want the foreign %q left untouched", head, foreign)
	}
	if got := p.publisher().PushCount(); got != 0 {
		t.Error("an expired run must not push; settlement only asks what already exists")
	}
}

// A publication that genuinely never began still has to be SETTLED: an intent
// left 'pending' forever reads as in-flight to everything that looks at it.
func TestExpiredRun_APublicationThatNeverLandedIsHaltedNotLeftPending(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().HangBeforePush()
	t.Cleanup(release)
	p.approve(t, "gate")

	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pending", 45*time.Second)
	backdateWallDeadline(t, p.f, p.run.ID)
	startAbsenceBackdater(t, p.f, p.run.ID)

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 90*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil {
		t.Fatalf("an intent that never published must be settled with a reason: %+v", intent)
	}
	if !strings.Contains(*intent.HaltReason, "no branch on the forge") {
		t.Errorf("halt reason = %q, want the branch-absence answer", *intent.HaltReason)
	}
	if p.publisher().PushCount() != 0 {
		t.Error("an expired run must not push")
	}
	// The settlement halt is the most common one in this whole path, and it is
	// written through a CAS rather than through haltPublication — so without a
	// shared emission path an operator reading the stream would see an intent
	// silently change status.
	if n := countEvents(t, p.svc, p.run.ID, runs.EventPublishHalted); n != 1 {
		t.Errorf("publish.halted events = %d, want 1: a settlement halt is a halt", n)
	}
	if n := scalarCount(t, p.f,
		`SELECT count(*) FROM audit_entries WHERE action = 'publish.halted' AND subject_id = $1`,
		intent.ID.String()); n != 1 {
		t.Errorf("publish.halted audit entries = %d, want 1", n)
	}
}

// The negative case's own race, and the one a single probe gets wrong. The push
// is issued just before the deadline and lands just after settlement first
// looks. If "absent" settled the intent on sight, the run would be dead and
// superseded by the time the branch appeared — an orphan on the forge that
// nothing ever examines again.
func TestExpiredRun_AnEffectLandingAfterANegativeProbeIsStillRecorded(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().DelayPushDelivery()
	t.Cleanup(release)
	p.approve(t, "gate")

	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pending", 45*time.Second)
	probed := p.forge().BranchProbeCount() // the live path's own recovery probe
	backdateWallDeadline(t, p.f, p.run.ID)

	// Settlement looks and finds nothing. It must NOT settle on that.
	waitForForgeBranchProbe(t, p.forge(), probed+1, 45*time.Second)
	if got := intentRow(t, p.f, p.run.ID, "publish").Status; got == "halted" {
		t.Fatal("one absent probe settled the intent; a push still on the wire is now an orphan")
	}
	if got := runStatus(t, p.f, p.run.ID); got == "dead" {
		t.Fatal("the run died on a single negative probe")
	}

	release() // the push lands, late
	// Only compress the grace once the effect has actually landed. Ageing the
	// stamp while a clone-and-push is still in flight would recreate, in the
	// test, the exact race the grace exists to survive — and the run would die
	// over the branch it is about to be handed.
	waitForPushCount(t, p.publisher(), 1, 45*time.Second)
	startAbsenceBackdater(t, p.f, p.run.ID)
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 120*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.HeadSHA == nil {
		t.Fatalf("the late push must be recorded, not orphaned: %+v", intent)
	}
	if got := remoteBranchHead(t, p.f, intent.Branch); got != *intent.HeadSHA {
		t.Errorf("recorded head %q does not match the remote's %q", *intent.HeadSHA, got)
	}
}

// A forge that cannot be reached is not an answer. The run stays alive and is
// asked again, because a wrong durable answer here has no later corrector.
func TestExpiredRun_AnUnreachableForgeDefersTerminalizationRatherThanGuessing(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().HangAfterPush()
	t.Cleanup(release)
	p.approve(t, "gate")
	waitForPushCount(t, p.publisher(), 1, 45*time.Second)

	p.forge().FailBranchProbes(true)
	probed := p.forge().BranchProbeCount()
	backdateWallDeadline(t, p.f, p.run.ID)

	// Wait for a POSITIVE signal — settlement ran and failed — rather than for
	// a fixed sleep, which would report "still alive" even if settlement had
	// never been called at all. Then wait for it to ask AGAIN, which is the
	// half that says the run is still being carried rather than merely slow to
	// die: a settlement that guessed would terminalize on the first refusal and
	// never probe a second time.
	waitForForgeBranchProbe(t, p.forge(), probed+1, 45*time.Second)
	if got := runStatus(t, p.f, p.run.ID); got == "dead" {
		t.Fatal("the run died while its publication was unknowable; the orphan is now permanent")
	}
	waitForForgeBranchProbe(t, p.forge(), probed+3, 45*time.Second)
	if got := runStatus(t, p.f, p.run.ID); got == "dead" {
		t.Fatal("the run died while its publication was unknowable; the orphan is now permanent")
	}
	if got := intentRow(t, p.f, p.run.ID, "publish").Status; got != "pending" {
		t.Fatalf("intent status = %q, want pending: nothing was knowable", got)
	}

	p.forge().FailBranchProbes(false)
	startAbsenceBackdater(t, p.f, p.run.ID)
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 90*time.Second)
	if intentRow(t, p.f, p.run.ID, "publish").HeadSHA == nil {
		t.Error("once the forge answered, the landed push must be recorded")
	}
}

// The other half of that ruling: deferring has a bound. A forge that is down
// for good — or a credential revoked after the intent was created — would
// otherwise leave the run alive past its deadline forever, re-probing on every
// tick. Past the bound the intent is halted with a reason that says exactly
// what is unknown.
func TestExpiredRun_AForgeThatStaysUnreachableHaltsRatherThanLivingForever(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().HangAfterPush()
	t.Cleanup(release)
	p.approve(t, "gate")
	waitForPushCount(t, p.publisher(), 1, 45*time.Second)

	p.forge().FailBranchProbes(true)
	// Past the deadline by more than the unverifiable bound: the run has been
	// owed an answer for longer than any outage this path will wait out.
	backdateWallDeadlineBy(t, p.f, p.run.ID, "31 minutes")

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 90*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil {
		t.Fatalf("an unverifiable publication must be settled with a reason: %+v", intent)
	}
	if !strings.Contains(*intent.HaltReason, "expired and unverifiable") {
		t.Errorf("halt reason = %q, want it to say the forge could not be reached", *intent.HaltReason)
	}
	if intent.HeadSHA != nil {
		t.Errorf("head_sha = %q, want none: nothing was ever verified", *intent.HeadSHA)
	}
}

// The SAME race as the branch case, one step later — and the one a branch-only
// stable negative misses completely. The intent is already 'pushed', so what is
// in flight at the deadline is the CREATE PULL REQUEST call.
func TestExpiredRun_APullRequestLandingAfterANegativeProbeIsStillRecorded(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.forge().DelayPullRequestDelivery()
	t.Cleanup(release)
	p.approve(t, "gate")

	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pushed", 45*time.Second)
	probed := p.forge().PullRequestProbeCount()
	backdateWallDeadline(t, p.f, p.run.ID)

	waitForForgePRProbe(t, p.forge(), probed+1, 45*time.Second)
	if got := intentRow(t, p.f, p.run.ID, "publish").Status; got == "halted" {
		t.Fatal("one absent PR probe settled the intent; the request on the wire is now an orphan")
	}

	release() // the pull request lands, late
	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 120*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.PRNumber == nil {
		t.Fatalf("the late pull request must be recorded, not orphaned: %+v", intent)
	}
	if intent.Status != "published" {
		t.Errorf("status = %q; an intent whose PR is recorded is published", intent.Status)
	}
	if got := p.forge().PullRequestCount(intent.Branch); got != 1 {
		t.Errorf("pull requests on %s = %d, want 1", intent.Branch, got)
	}
}

// The other half of the same rule: deferring whenever no request is visible
// would strand every run that genuinely died between its push and its create
// call. Absence must still settle — after the grace, not on sight.
func TestExpiredRun_APublicationThatNeverOpenedAPullRequestStillSettles(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.forge().HangBeforePullRequest()
	t.Cleanup(release)
	p.approve(t, "gate")

	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pushed", 45*time.Second)
	backdateWallDeadline(t, p.f, p.run.ID)
	startAbsenceBackdater(t, p.f, p.run.ID)

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 90*time.Second)

	intent := intentRow(t, p.f, p.run.ID, "publish")
	if intent.Status != "halted" || intent.HaltReason == nil {
		t.Fatalf("a publication that never opened a request must be halted with a reason: %+v", intent)
	}
	if !strings.Contains(*intent.HaltReason, "no pull request") {
		t.Errorf("halt reason = %q, want the pull-request absence answer", *intent.HaltReason)
	}
	if intent.HeadSHA == nil {
		t.Error("halting the PR side must not discard the push receipt that did land")
	}
	if got := p.forge().OpenPRCount(); got != 0 {
		t.Errorf("the forge accepted %d creates, want 0", got)
	}
}

// A regression guard for the quiescence definition. Fencing leaves every phase
// 'reconciling', which is NOT terminal, so a quiescence test spelled "no live
// phase row" would never pass on exactly the runs this path exists for:
// settlement would defer forever and the run would never die.
func TestExpiredRun_SettlementRunsWhileItsPhasesAreReconciling(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	release := p.publisher().HangAfterPush()
	t.Cleanup(release)
	p.approve(t, "gate")
	waitForPushCount(t, p.publisher(), 1, 45*time.Second)

	backdateWallDeadline(t, p.f, p.run.ID)
	waitForPhaseStatus(t, p.f, p.run.ID, "publish", string(runs.PhaseReconciling), 45*time.Second)
	startAbsenceBackdater(t, p.f, p.run.ID)

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 90*time.Second)
	if intentRow(t, p.f, p.run.ID, "publish").HeadSHA == nil {
		t.Error("the landed push must be recorded even though its phase was mid-reconcile")
	}
}

// Fencing is the run's own obligation, and this is the deployment that proves
// it: production wires an executor that holds no credentials, so a fencing pass
// owned by the credential sweep would short-circuit — nothing fenced,
// quiescence never true, and an expired run that can never die.
func TestExpiredRun_SettlesWithoutACredentialReconcilerWired(t *testing.T) {
	p := publicationAtItsGate(t, gatedConveyorDoc())
	p.stop()
	p.approve(t, "gate")
	release := p.publisher().HangBeforePush()
	t.Cleanup(release)
	startStoppableOrchestrator(t, p.svc, noCredentialExecutor{p.ex}, publishLease)

	waitForIntentStatus(t, p.f, p.run.ID, "publish", "pending", 45*time.Second)
	backdateWallDeadline(t, p.f, p.run.ID)
	startAbsenceBackdater(t, p.f, p.run.ID)

	waitForRunWithin(t, p.svc, p.run.ID, runs.RunDead, 90*time.Second)
	if got := intentRow(t, p.f, p.run.ID, "publish").Status; got != "halted" {
		t.Errorf("intent status = %q, want halted: an expired run must settle and die whether or "+
			"not this deployment's executor holds credentials", got)
	}
}
