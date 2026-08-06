package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// journal is the one ordered record both fakes append to. Reconciliation's
// contract is an ORDER — take custody, stop the runtime, only then settle —
// and an order is only observable against a single sequence.
type journal struct {
	mu sync.Mutex
	ev []string
}

func (j *journal) add(what string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ev = append(j.ev, what)
}

func (j *journal) events() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.ev...)
}

// before asserts that one event happened, that the other happened, and that
// the first came first — the three failures a bare index comparison would
// collapse into one confusing message.
func (j *journal) before(t *testing.T, first, second string) {
	t.Helper()
	ev := j.events()
	i, j2 := slices.Index(ev, first), slices.Index(ev, second)
	switch {
	case i < 0:
		t.Errorf("%q never happened; the sequence was %v", first, ev)
	case j2 < 0:
		t.Errorf("%q never happened; the sequence was %v", second, ev)
	case i > j2:
		t.Errorf("%q happened after %q; the sequence was %v", first, second, ev)
	}
}

// reconEngine is a daemon reconciliation meets after a crash: one agent
// container in a scripted state, and a record of everything it was asked.
type reconEngine struct {
	Engine // embed nil: an unexpected call panics loudly

	j *journal

	mu        sync.Mutex
	agent     dockerapi.ContainerInspect
	gone      bool  // the agent container is not there at all
	removeErr error // the daemon refuses to remove anything
	removed   []string
	created   []string
}

func (e *reconEngine) ContainerInspect(_ context.Context, name string) (dockerapi.ContainerInspect, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.j.add("inspect:" + name)
	if e.gone {
		return dockerapi.ContainerInspect{}, dockerapi.NotFound("no such container: " + name)
	}
	return e.agent, nil
}

func (e *reconEngine) ContainerKill(_ context.Context, name, _ string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.j.add("kill:" + name)
	// A kill the daemon accepted leaves a corpse behind, exactly as the real
	// one does: everything after this sees an exited container.
	e.agent.State.Status = "exited"
	e.agent.State.ExitCode = 137
	return nil
}

func (e *reconEngine) ContainerWait(_ context.Context, name string) (int64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.j.add("wait:" + name)
	return e.agent.State.ExitCode, nil
}

func (e *reconEngine) ContainerRemove(_ context.Context, name string, _ bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.j.add("remove:" + name)
	if e.removeErr != nil {
		return e.removeErr
	}
	e.removed = append(e.removed, name)
	e.gone = true
	return nil
}

func (e *reconEngine) ContainerCreate(_ context.Context, name string, _ dockerapi.ContainerConfig,
	_ dockerapi.HostConfig, _ map[string]dockerapi.EndpointSettings,
) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.j.add("create:" + name)
	e.created = append(e.created, name)
	return "id-" + name, nil
}

func (e *reconEngine) NetworkRemove(context.Context, string) error { return nil }

func (e *reconEngine) removedNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.removed...)
}

func (e *reconEngine) createdNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.created...)
}

// reconFixture is one crashed attempt as reconciliation finds it: a runner, a
// scripted daemon and leaser sharing an ordering journal, and the attempt's
// credential material on disk.
type reconFixture struct {
	r      *Runner
	eng    *reconEngine
	leaser *fakeLeaser
	j      *journal
	ref    runs.AttemptRef
	credID uuid.UUID
}

func newReconFixture(t *testing.T, status string, exitCode int64) *reconFixture {
	t.Helper()
	j := &journal{}
	credID, ref := uuid.New(), testRef()
	eng := &reconEngine{j: j}
	eng.agent.State.Status = status
	eng.agent.State.ExitCode = exitCode
	leaser := &fakeLeaser{
		held: true, leaseCredential: credID, j: j,
		lease: Lease{Epoch: 1, SecretVersion: 9},
	}
	r := newLeaseRunner(t, eng, leaser, &recordingOpener{}, Options{DataRoot: t.TempDir()})
	return &reconFixture{r: r, eng: eng, leaser: leaser, j: j, ref: ref, credID: credID}
}

// seedCrashedAttempt puts on disk what a worker that died mid-attempt left: the
// staged attempt directory the collection reads, the agent's log, and the
// credential material the conclusion reads back.
func (f *reconFixture) seedCrashedAttempt(t *testing.T, agentLog string) {
	t.Helper()
	dir := f.r.attemptDir(f.ref)
	if err := os.MkdirAll(filepath.Join(dir, gitops.WorkspaceDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeContract(filepath.Join(dir, gitops.StageInputFile),
		gitops.StageInput{CloneURL: "file:///remote/bare.git", BaseSHA: "abc"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.r.logsDir(f.ref), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.r.logsDir(f.ref), agentLogFile), []byte(agentLog), 0o600); err != nil {
		t.Fatal(err)
	}
	seedSyncFixture(t, f.r, f.ref, f.credID)
}

// The ordering the whole recovery story rests on (spec §2.1, §2.3, §2.8):
// custody is taken BEFORE the container is touched — otherwise the paused
// worker could still commit or clean up — and the credential is only read back
// and released AFTER the runtime is provably stopped, or a live agent could
// write auth.json between the read and the commit.
func TestInspect_TakesCustodyBeforeTheKillAndSettlesOnlyAfterIt(t *testing.T) {
	f := newReconFixture(t, "running", 0)
	f.seedCrashedAttempt(t, "still going")
	rewriteAuthFile(t, f.r, f.ref, refreshedCodexJSON)

	res, err := f.r.Inspect(t.Context(), f.ref)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if res.Completed {
		t.Errorf("a container this reconciliation killed was adopted: %+v", res)
	}

	f.j.before(t, "takeover", "kill:"+agentName(f.ref))
	f.j.before(t, "kill:"+agentName(f.ref), "wait:"+agentName(f.ref))
	f.j.before(t, "wait:"+agentName(f.ref), "commit")
	f.j.before(t, "remove:"+agentName(f.ref), "commit")
	f.j.before(t, "commit", "release")

	if takeovers := f.leaser.takeoverCalls(); len(takeovers) != 1 ||
		takeovers[0].credentialID != f.credID || takeovers[0].ref != f.ref.String() {
		t.Errorf("takeover calls = %+v, want exactly one for this attempt's credential", takeovers)
	}
	// The conclusion runs under the epoch the takeover advanced to, not the
	// one the dead worker held: a commit at the old fence is refused.
	commits := f.leaser.commitCalls()
	if len(commits) != 1 || commits[0].epoch != 2 || commits[0].expectVersion != 9 {
		t.Fatalf("commits = %+v, want one at the taken-over epoch 2, version 9", commits)
	}
	if _, _, released := f.leaser.calls(); len(released) != 1 || released[0].epoch != 2 {
		t.Errorf("releases = %+v, want exactly one at the taken-over epoch", released)
	}
}

// The crash-after-a-cap-hit case, now with a credential behind it: settlement
// must not cost the attempt its adoptability, and adoption must still read the
// exit through the right marker table — off the in-memory manifest, because the
// conclusion has already deleted the file.
func TestInspect_ExitedNonzeroIsStillAdoptedWithItsTypedOutcomeAfterSettling(t *testing.T) {
	f := newReconFixture(t, "exited", 1)
	reset := time.Now().Add(30 * time.Minute).UTC().Truncate(time.Second)
	f.seedCrashedAttempt(t, fmt.Sprintf("usage limit reached|%d\n", reset.Unix()))

	res, err := f.r.Inspect(t.Context(), f.ref)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !res.Completed || res.Outcome == nil {
		t.Fatalf("result = %+v, want the typed failure adopted", res)
	}
	if res.Outcome.Code != runs.CodeRateLimited {
		t.Errorf("code = %q, want rate_limited — adoption read the manifest it settled against", res.Outcome.Code)
	}
	if res.Outcome.RetryAt == nil || !res.Outcome.RetryAt.Equal(reset) {
		t.Errorf("RetryAt = %v, want the marker's own %s", res.Outcome.RetryAt, reset)
	}
	// The CLI never wrote, so the conclusion is clean — and clean still means
	// the lease came back and the evidence left the host.
	if _, _, released := f.leaser.calls(); len(released) != 1 {
		t.Errorf("releases = %+v, want exactly one: an adopted attempt still owes its credential", released)
	}
	assertAuthEvidence(t, f.r, f.ref, false)
}

// The teardown race carried forward from Task 5: a worker whose heartbeat was
// fenced unwinds through cancellation-immune teardown and deletes the
// containers and the attempt directory out from under the reconciler that
// superseded it. Reconciliation TOLERATES that rather than suppressing the
// teardown — a fenced worker that skipped teardown would leave a live agent
// running against a credential this reconciler is about to release. What it may
// never do is lose the obligation: the auth directory and the manifest live
// outside the attempt tree precisely so teardown cannot reach them.
func TestInspect_SettlesEvenWhenAFencedWorkerAlreadyToreTheAttemptDown(t *testing.T) {
	f := newReconFixture(t, "exited", 0)
	f.seedCrashedAttempt(t, "done")
	rewriteAuthFile(t, f.r, f.ref, refreshedCodexJSON)
	// Exactly what the fenced worker's teardown leaves: no containers, no
	// attempt directory. The credential material is untouched.
	f.eng.gone = true
	if err := os.RemoveAll(f.r.attemptDir(f.ref)); err != nil {
		t.Fatal(err)
	}

	res, err := f.r.Inspect(t.Context(), f.ref)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if res.Completed {
		t.Errorf("an attempt whose workspace is gone was adopted: %+v", res)
	}
	f.j.before(t, "takeover", "commit")
	if commits := f.leaser.commitCalls(); len(commits) != 1 {
		t.Fatalf("commits = %+v, want the refreshed credential committed anyway", commits)
	}
	if _, _, released := f.leaser.calls(); len(released) != 1 {
		t.Errorf("releases = %+v, want exactly one: a torn-down attempt still owes its credential", released)
	}
}

// A crash between the lease commit and the manifest write (spec §2.3): the
// lease row is the recovery authority, so the obligation resolves to a release
// rather than to a credential_busy nobody can ever conclude.
func TestInspect_ManifestlessLeaseIsStillReleased(t *testing.T) {
	f := newReconFixture(t, "exited", 0)
	f.eng.gone = true

	if _, err := f.r.Inspect(t.Context(), f.ref); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if commits := f.leaser.commitCalls(); len(commits) != 0 {
		t.Errorf("commits = %+v, want none: there is no read-back without a manifest", commits)
	}
	_, _, released := f.leaser.calls()
	if len(released) != 1 || released[0].credentialID != f.credID || released[0].epoch != 2 {
		t.Errorf("releases = %+v, want one at the taken-over epoch for the credential the LEASE named", released)
	}
}

// Custody that cannot be taken is custody nobody has: touching the container or
// the credential now would race a worker this reconciliation has not fenced, so
// the whole attempt is reported lost and retried instead.
func TestInspect_RefusesToActWhenCustodyCannotBeTaken(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sabotage func(l *fakeLeaser)
	}{
		{"the lease cannot be looked up", func(l *fakeLeaser) { l.lookupErr = errors.New("connection reset") }},
		{"the takeover is refused", func(l *fakeLeaser) { l.takeoverErr = errors.New("connection reset") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReconFixture(t, "running", 0)
			f.seedCrashedAttempt(t, "still going")
			tc.sabotage(f.leaser)

			if _, err := f.r.Inspect(t.Context(), f.ref); err == nil {
				t.Fatal("want a transient error the orchestrator concludes as reconcile_lost")
			}
			if ev := f.j.events(); slices.Contains(ev, "kill:"+agentName(f.ref)) {
				t.Errorf("the container was killed without custody; the sequence was %v", ev)
			}
			if _, _, released := f.leaser.calls(); len(released) != 0 {
				t.Errorf("releases = %+v, want none: this reconciliation owns nothing", released)
			}
		})
	}
}

// The janitor entry settles the credential and NOTHING else. It never adopts:
// its callers have already decided the attempt's outcome, and a result
// collected here could be persisted as a success the run is not entitled to.
func TestReconcileCredential_SettlesWithoutAdopting(t *testing.T) {
	f := newReconFixture(t, "exited", 0)
	f.seedCrashedAttempt(t, "done")
	rewriteAuthFile(t, f.r, f.ref, refreshedCodexJSON)

	if err := f.r.ReconcileCredential(t.Context(), f.ref); err != nil {
		t.Fatalf("ReconcileCredential: %v", err)
	}
	f.j.before(t, "takeover", "remove:"+agentName(f.ref))
	f.j.before(t, "remove:"+agentName(f.ref), "commit")
	f.j.before(t, "commit", "release")

	if got := f.eng.removedNames(); !slices.Contains(got, agentName(f.ref)) || !slices.Contains(got, gatewayName(f.ref)) {
		t.Errorf("removed %v, want the agent and the gateway — the two containers still running attempt code", got)
	}
	if created := f.eng.createdNames(); len(created) != 0 {
		t.Errorf("the cleanup-only path created %v, want nothing: it never collects a result", created)
	}
	if commits := f.leaser.commitCalls(); len(commits) != 1 || commits[0].epoch != 2 {
		t.Errorf("commits = %+v, want the refreshed credential committed once at the taken-over epoch", commits)
	}
	if _, _, released := f.leaser.calls(); len(released) != 1 {
		t.Errorf("releases = %+v, want exactly one", released)
	}
	// The attempt's own workspace is not this path's business — the orphan
	// sweep owns it — but the credential evidence it did conclude is gone.
	assertAuthEvidence(t, f.r, f.ref, false)
}

// The settlement's precondition, enforced rather than assumed: a runtime the
// daemon will not stop may still be writing auth.json, so nothing is read back
// and nothing is released. The caller retries, and the lease stays held under
// this reconciler's own epoch in the meantime.
func TestReconcileCredential_WillNotSettleOverARuntimeItCannotStop(t *testing.T) {
	f := newReconFixture(t, "running", 0)
	f.seedCrashedAttempt(t, "still going")
	rewriteAuthFile(t, f.r, f.ref, refreshedCodexJSON)
	f.eng.removeErr = errors.New("daemon is wedged")

	if err := f.r.ReconcileCredential(t.Context(), f.ref); err == nil {
		t.Fatal("want an error: the caller must not terminalize over an unsettled obligation")
	}
	if commits := f.leaser.commitCalls(); len(commits) != 0 {
		t.Errorf("commits = %+v, want none: a live agent may still be writing that file", commits)
	}
	if _, _, released := f.leaser.calls(); len(released) != 0 {
		t.Errorf("releases = %+v, want none: releasing would let a second attempt start beside the first", released)
	}
	assertAuthEvidence(t, f.r, f.ref, true)
}

// An attempt with no lease behind it still hands adoption what it needs: the
// manifest read and the pre-commit open are not gated on the lease, because a
// static kind writes a manifest with no lease at all and its logs still have to
// be scannable for the bytes the container saw (Task 11).
func TestSettleCredential_CapturesTheRedactionInputsWithoutALease(t *testing.T) {
	f := newReconFixture(t, "exited", 0)
	f.leaser.held, f.leaser.leaseCredential = false, uuid.Nil
	if err := f.r.writeManifest(f.ref, attemptManifest{
		CredentialID: f.credID.String(), Kind: "subscription_token",
		Custody: custodyMaterialized, HarnessKind: "claude-code", SecretVersion: openedSecretVersion,
	}); err != nil {
		t.Fatal(err)
	}

	rec := f.r.settleCredential(t.Context(), f.ref, uuid.Nil, Lease{}, false)

	if !rec.HasManifest || rec.Manifest.HarnessKind != "claude-code" {
		t.Errorf("reconciliation = %+v, want the manifest it found on disk", rec)
	}
	if string(rec.Stored) != "provider-key" || !rec.StoredOK {
		t.Errorf("stored = %q ok=%v, want the opened bytes at the version the manifest recorded", rec.Stored, rec.StoredOK)
	}
	if rec.Concluded {
		t.Error("a conclusion ran with no lease to conclude")
	}
}

// A store that has rotated past the version the attempt was handed cannot give
// those bytes back, so adoption must be told it has nothing to scan with —
// whether or not the kind takes a lease.
func TestSettleCredential_StaleVersionIsNotRecoverable(t *testing.T) {
	f := newReconFixture(t, "exited", 0)
	f.leaser.held, f.leaser.leaseCredential = false, uuid.Nil
	if err := f.r.writeManifest(f.ref, attemptManifest{
		CredentialID: f.credID.String(), Kind: "subscription_token",
		Custody: custodyMaterialized, HarnessKind: "claude-code", SecretVersion: openedSecretVersion + 1,
	}); err != nil {
		t.Fatal(err)
	}

	rec := f.r.settleCredential(t.Context(), f.ref, uuid.Nil, Lease{}, false)

	if rec.StoredOK {
		t.Error("the delivered version is gone from the store, yet adoption was told it could scan")
	}
}

// The evidence a conclusion deliberately retained (unscannable, quarantined)
// has no other reaper. The gate is the LEASE, not the age: anything a
// reconciler could still come back for is left alone however old it is.
func TestSweepOrphans_ReapsUnleasedCredentialLeftoversOnly(t *testing.T) {
	j := &journal{}
	leaser := &fakeLeaser{j: j}
	r := newLeaseRunner(t, &reconEngine{j: j}, leaser, nil, Options{DataRoot: t.TempDir()})

	held, orphan, fresh := testRef(), runs.AttemptRef{RunID: uuid.New(), NodeID: "impl", Attempt: 1}, runs.AttemptRef{RunID: uuid.New(), NodeID: "plan", Attempt: 1}
	for _, ref := range []runs.AttemptRef{held, orphan, fresh} {
		seedAuthDir(t, r, ref)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, ref := range []runs.AttemptRef{held, orphan} {
		for _, p := range []string{r.authDir(ref), r.manifestPath(ref)} {
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The held one is a reconciler's evidence, not an orphan.
	leaser.held, leaser.leaseCredential = true, uuid.New()
	leaser.lookupRefs = map[string]bool{held.String(): true}

	removed, err := r.sweepCredentialLeftovers(t.Context(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 2 {
		t.Errorf("swept %d leftovers, want 2 — the orphan's auth dir and its manifest", removed)
	}
	assertAuthEvidence(t, r, held, true)
	assertAuthEvidence(t, r, fresh, true)
	assertAuthEvidence(t, r, orphan, false)
}

// A runner that cannot ask about leases cannot prove a leftover is unheld, and
// an unproven removal is exactly what the gate exists to stop.
func TestSweepOrphans_LeaserlessRunnerReapsNoCredentialLeftovers(t *testing.T) {
	r := newLeaseRunner(t, &reconEngine{j: &journal{}}, nil, nil, Options{DataRoot: t.TempDir()})
	ref := testRef()
	seedAuthDir(t, r, ref)
	old := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{r.authDir(ref), r.manifestPath(ref)} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := r.sweepCredentialLeftovers(t.Context(), time.Now().Add(-24*time.Hour))
	if err != nil || removed != 0 {
		t.Errorf("swept %d leftovers (err %v), want 0", removed, err)
	}
	assertAuthEvidence(t, r, ref, true)
}
