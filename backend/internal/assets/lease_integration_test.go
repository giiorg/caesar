//go:build integration

package assets_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/assets"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// leaseRow reads the lease straight from the table: assertions about what
// actually committed must not go through the code under test.
func leaseRow(t *testing.T, pool *pgxpool.Pool, credID uuid.UUID) (epoch, version int64, heartbeatAt time.Time, held bool) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		"SELECT epoch, secret_version, heartbeat_at FROM credential_leases WHERE credential_id = $1", credID).
		Scan(&epoch, &version, &heartbeatAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, time.Time{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return epoch, version, heartbeatAt, true
}

// backdateLease pushes heartbeat_at into the past — the SQL-backdating
// precedent for time-dependent predicates — and returns the value written.
func backdateLease(t *testing.T, pool *pgxpool.Pool, credID uuid.UUID, by time.Duration) time.Time {
	t.Helper()
	var at time.Time
	if err := pool.QueryRow(context.Background(),
		"UPDATE credential_leases SET heartbeat_at = now() - make_interval(secs => $2) WHERE credential_id = $1 RETURNING heartbeat_at",
		credID, by.Seconds()).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func backdateLastUsed(t *testing.T, pool *pgxpool.Pool, credID uuid.UUID, by time.Duration) time.Time {
	t.Helper()
	var at time.Time
	if err := pool.QueryRow(context.Background(),
		"UPDATE credentials SET last_used_at = now() - make_interval(secs => $2) WHERE id = $1 RETURNING last_used_at",
		credID, by.Seconds()).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func credCiphertext(t *testing.T, pool *pgxpool.Pool, credID uuid.UUID) []byte {
	t.Helper()
	var ct []byte
	if err := pool.QueryRow(context.Background(),
		"SELECT ciphertext FROM credentials WHERE id = $1", credID).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	return ct
}

// auditDetail returns one JSON detail field of the single audit row for
// action, so tests can assert why a decision was recorded, not just that
// something was.
func auditDetail(t *testing.T, pool *pgxpool.Pool, action, key string) string {
	t.Helper()
	var v *string
	if err := pool.QueryRow(context.Background(),
		"SELECT detail->>$2 FROM audit_entries WHERE action = $1", action, key).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatalf("audit %s has no %q detail", action, key)
	}
	return *v
}

// syncReceipt reads the receipt row for an attempt. The epoch it carries is
// load-bearing: it says whose bytes are in the store, not merely that some
// commit happened.
func syncReceipt(t *testing.T, pool *pgxpool.Pool, credID uuid.UUID, attemptRef string) (epoch, from, to int64) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		"SELECT epoch, from_version, to_version FROM credential_sync_receipts WHERE credential_id = $1 AND attempt_ref = $2",
		credID, attemptRef).Scan(&epoch, &from, &to); err != nil {
		t.Fatal(err)
	}
	return epoch, from, to
}

func receiptCount(t *testing.T, pool *pgxpool.Pool, credID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM credential_sync_receipts WHERE credential_id = $1", credID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedAuthFile creates an agent_auth_file credential holding refresh.
func seedAuthFile(t *testing.T, svc *assets.Service, admin authn.Principal, name, refresh string) (assets.Credential, []byte) {
	t.Helper()
	body := authJSON(t, refresh, 0)
	c, err := svc.CreateCredential(context.Background(), admin, name, assets.CredentialAgentAuthFile, body)
	if err != nil {
		t.Fatal(err)
	}
	return c, body
}

// Scenario 1 (spec §2.1): the lease is the single-flight mechanism, and it
// is idempotent for the attempt that already holds it — a retried attempt
// must not deadlock against its own lease.
func TestAcquireCredentialLease_SingleFlightAndIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, body := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	if got := credLastUsed(t, pool, c.ID); got != nil {
		t.Fatalf("last_used_at = %v before any acquire, want NULL", got)
	}

	lease, secret, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if lease.CredentialID != c.ID || lease.AttemptRef != "attempt-a" || lease.Epoch != 1 || lease.SecretVersion != 1 {
		t.Fatalf("lease = %+v, want attempt-a holding epoch 1 at version 1", lease)
	}
	if !bytes.Equal(secret, body) {
		t.Fatalf("snapshot = %q, want the stored auth.json", secret)
	}
	// Leased kinds never pass through UseCredential, so acquisition is what
	// the §5 freshness view sees.
	firstUse := credLastUsed(t, pool, c.ID)
	if firstUse == nil {
		t.Fatal("acquire must stamp last_used_at")
	}

	// A different attempt cannot hold the same credential.
	_, _, err = svc.AcquireCredentialLease(ctx, c.ID, "attempt-b")
	if !errors.Is(err, assets.ErrLeaseBusy) {
		t.Fatalf("busy acquire = %v, want ErrLeaseBusy", err)
	}
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("busy acquire class = %v, want conflict", xerrors.ClassOf(err))
	}
	if got := credLastUsed(t, pool, c.ID); !got.Equal(*firstUse) {
		t.Fatalf("last_used_at = %v after a refused acquire, want the losing tx rolled back to %v", got, firstUse)
	}

	// Backdating forces the re-acquire's stamp to prove it moved.
	stale := backdateLastUsed(t, pool, c.ID, time.Hour)
	again, secretAgain, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if again.Epoch != lease.Epoch || again.SecretVersion != lease.SecretVersion || again.AttemptRef != lease.AttemptRef {
		t.Fatalf("re-acquire = %+v, want the same lease as %+v", again, lease)
	}
	if !bytes.Equal(secretAgain, body) {
		t.Fatalf("re-acquire snapshot = %q, want the stored auth.json", secretAgain)
	}
	if got := credLastUsed(t, pool, c.ID); got == nil || !got.After(stale) {
		t.Fatalf("last_used_at = %v after re-acquire, want later than %v", got, stale)
	}
	if epoch, version, _, held := leaseRow(t, pool, c.ID); !held || epoch != 1 || version != 1 {
		t.Fatalf("lease row = (epoch %d, version %d, held %v), want one row at epoch 1 version 1", epoch, version, held)
	}

	// Every acquire hands back plaintext, so every acquire is an audited
	// use — including the idempotent one.
	if n := auditCount(t, pool, "credential.lease_acquired"); n != 2 {
		t.Fatalf("credential.lease_acquired audits = %d, want one per successful acquire", n)
	}
	var actorIsNull bool
	if err := pool.QueryRow(ctx,
		"SELECT bool_and(actor_user_id IS NULL) FROM audit_entries WHERE action = 'credential.lease_acquired'").Scan(&actorIsNull); err != nil {
		t.Fatal(err)
	}
	if !actorIsNull {
		t.Fatal("credential.lease_acquired audits must have a NULL actor_user_id (machine path)")
	}
}

// Scenario 1b: an idempotent re-acquire re-binds the lease to the version
// it is about to hand over — the snapshot and lease.secret_version are one
// atomic read, so a sync-back CAS can never be discarded for a rotation the
// caller never saw.
func TestAcquireCredentialLease_ReacquireRebindsToCurrentVersion(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	if _, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a"); err != nil {
		t.Fatal(err)
	}
	rotated := authJSON(t, "rt-2", 0)
	if _, err := svc.RotateCredentialSecret(ctx, admin, c.ID, rotated); err != nil {
		t.Fatal(err)
	}

	again, secret, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if again.SecretVersion != 2 || !bytes.Equal(secret, rotated) {
		t.Fatalf("re-acquire = version %d / %q, want version 2 and the rotated bytes", again.SecretVersion, secret)
	}
	if _, version, _, _ := leaseRow(t, pool, c.ID); version != 2 {
		t.Fatalf("lease row version = %d, want the version the snapshot came from", version)
	}
}

// A takeover moves custody to recovery, so the superseded worker cannot
// take its own lease back — otherwise it would resume under a lease the
// reconciler is in the middle of concluding.
func TestAcquireCredentialLease_FencedAfterTakeover(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	if _, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TakeoverCredentialLease(ctx, c.ID, "attempt-a"); err != nil {
		t.Fatal(err)
	}

	_, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if !errors.Is(err, assets.ErrLeaseBusy) {
		t.Fatalf("re-acquire of a taken-over lease = %v, want ErrLeaseBusy", err)
	}
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("re-acquire class = %v, want conflict", xerrors.ClassOf(err))
	}
	epoch, version, _, held := leaseRow(t, pool, c.ID)
	if !held || epoch != 2 || version != 1 {
		t.Fatalf("lease row = (epoch %d, version %d, held %v), want the reconciler's epoch 2 untouched", epoch, version, held)
	}
	var owner string
	if err := pool.QueryRow(ctx,
		"SELECT attempt_ref FROM credential_leases WHERE credential_id = $1", c.ID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "attempt-a" {
		t.Fatalf("lease attempt_ref = %q, want attempt-a", owner)
	}
}

// One attempt holds at most one credential lease, enforced by the unique
// index on attempt_ref — the recovery lookup by attempt has to be
// unambiguous.
func TestAcquireCredentialLease_OneLeasePerAttempt(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	a, _ := seedAuthFile(t, svc, admin, "codex-a", "rt-a")
	b, _ := seedAuthFile(t, svc, admin, "codex-b", "rt-b")

	if _, _, err := svc.AcquireCredentialLease(ctx, a.ID, "attempt-a"); err != nil {
		t.Fatal(err)
	}
	_, _, err := svc.AcquireCredentialLease(ctx, b.ID, "attempt-a")
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("second credential for one attempt = %v, want conflict", err)
	}
	if !strings.Contains(err.Error(), "another credential") {
		t.Fatalf("conflict = %q, want it to name the real cause", err)
	}
	if _, _, _, held := leaseRow(t, pool, b.ID); held {
		t.Fatal("the refused acquire must leave no lease behind")
	}
}

// Scenario 2 (spec §2.1): a heartbeat under a superseded epoch is the
// paused old worker talking; it must not keep the lease alive.
func TestHeartbeatCredentialLease_FencedByEpoch(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	lease, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.HeartbeatCredentialLease(ctx, c.ID, "attempt-a", lease.Epoch+1); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("heartbeat at a future epoch = %v, want conflict", err)
	}
	if err := svc.HeartbeatCredentialLease(ctx, c.ID, "attempt-b", lease.Epoch); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("heartbeat from another attempt = %v, want conflict", err)
	}

	stale := backdateLease(t, pool, c.ID, time.Hour)
	if err := svc.HeartbeatCredentialLease(ctx, c.ID, "attempt-a", lease.Epoch); err != nil {
		t.Fatal(err)
	}
	_, _, beat, held := leaseRow(t, pool, c.ID)
	if !held || !beat.After(stale) {
		t.Fatalf("heartbeat_at = %v (held %v), want later than %v", beat, held, stale)
	}
}

// Scenario 3 (spec §2.1): reconciliation advances the epoch before it
// touches the container, so the worker it took over from can never commit
// the snapshot it was holding.
func TestTakeoverCredentialLease_FencesTheOldOwnersCommit(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	lease, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	taken, err := svc.TakeoverCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if taken.Epoch != lease.Epoch+1 || taken.SecretVersion != lease.SecretVersion {
		t.Fatalf("takeover = %+v, want epoch %d at the granted version", taken, lease.Epoch+1)
	}
	if n := auditCount(t, pool, "credential.lease_takeover"); n != 1 {
		t.Fatalf("credential.lease_takeover audits = %d, want 1", n)
	}

	refreshed := authJSON(t, "rt-2", 0)
	before := credCiphertext(t, pool, c.ID)
	status, err := svc.CommitCredentialSync(ctx, c.ID, "attempt-a", lease.Epoch, lease.SecretVersion, refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if status != assets.SyncFenced {
		t.Fatalf("commit at the superseded epoch = %q, want fenced", status)
	}
	if v, _, _ := credentialSecretState(t, pool, c.ID); v != 1 {
		t.Fatalf("secret_version = %d after a fenced commit, want the store untouched at 1", v)
	}
	if !bytes.Equal(credCiphertext(t, pool, c.ID), before) {
		t.Fatal("a fenced commit must not rewrite the ciphertext")
	}
	if n := receiptCount(t, pool, c.ID); n != 0 {
		t.Fatalf("sync receipts = %d after a fenced commit, want 0", n)
	}

	// The reconciler holds the current epoch, so its commit lands.
	status, err = svc.CommitCredentialSync(ctx, c.ID, "attempt-a", taken.Epoch, taken.SecretVersion, refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if status != assets.SyncApplied {
		t.Fatalf("commit at the current epoch = %q, want applied", status)
	}
	if v, _, _ := credentialSecretState(t, pool, c.ID); v != 2 {
		t.Fatalf("secret_version = %d after the reconciler's commit, want 2", v)
	}
}

// Scenario 4 (spec §2.6): the receipt makes the commit-then-crash retry
// recognizable as already applied, so it can never bump the version twice.
func TestCommitCredentialSync_ReceiptMakesTheRetryIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, body := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	lease, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}

	// A malformed read-back is permanent, not a CAS race: the caller must
	// be told to release rather than retry, and the store stays as it was.
	if _, err := svc.CommitCredentialSync(ctx, c.ID, "attempt-a", lease.Epoch, lease.SecretVersion,
		[]byte("not-an-auth-file")); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("commit of a malformed auth.json = %v, want invalid", err)
	}
	if v, _, _ := credentialSecretState(t, pool, c.ID); v != 1 {
		t.Fatalf("secret_version = %d after a rejected commit, want 1", v)
	}

	refreshed := authJSON(t, "rt-2", 0)
	status, err := svc.CommitCredentialSync(ctx, c.ID, "attempt-a", lease.Epoch, lease.SecretVersion, refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if status != assets.SyncApplied {
		t.Fatalf("first commit = %q, want applied", status)
	}

	status, err = svc.CommitCredentialSync(ctx, c.ID, "attempt-a", lease.Epoch, lease.SecretVersion, refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if status != assets.SyncAlreadyApplied {
		t.Fatalf("replayed commit = %q, want already_applied", status)
	}
	if v, _, _ := credentialSecretState(t, pool, c.ID); v != 2 {
		t.Fatalf("secret_version = %d, want exactly one bump across two identical commits", v)
	}
	if n := auditCount(t, pool, "credential.synced"); n != 1 {
		t.Fatalf("credential.synced audits = %d, want 1", n)
	}

	plain, err := svc.UseCredential(ctx, c.ID, "verify-sync")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, refreshed) || bytes.Equal(plain, body) {
		t.Fatalf("stored secret = %q, want the refreshed auth.json", plain)
	}
}

// The receipt is keyed by (credential, attempt) but guards per EPOCH: a
// reconciler that takes over commits under the same attempt ref, and its
// fresher bytes must not be swallowed as a replay of the worker's commit.
func TestCommitCredentialSync_ReceiptIsEpochScoped(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	lease, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	workerBytes := authJSON(t, "rt-worker", 0)
	status, err := svc.CommitCredentialSync(ctx, c.ID, "attempt-a", lease.Epoch, lease.SecretVersion, workerBytes)
	if err != nil {
		t.Fatal(err)
	}
	if status != assets.SyncApplied {
		t.Fatalf("worker commit = %q, want applied", status)
	}
	if epoch, from, to := syncReceipt(t, pool, c.ID, "attempt-a"); epoch != 1 || from != 1 || to != 2 {
		t.Fatalf("receipt = (epoch %d, %d→%d), want epoch 1 and 1→2", epoch, from, to)
	}

	// Reconciliation takes over and reads a still-fresher auth.json off the
	// mount — the CLI refreshed again after the worker's commit.
	taken, err := svc.TakeoverCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	reconcilerBytes := authJSON(t, "rt-reconciler", 0)
	status, err = svc.CommitCredentialSync(ctx, c.ID, "attempt-a", taken.Epoch, 2, reconcilerBytes)
	if err != nil {
		t.Fatal(err)
	}
	if status != assets.SyncApplied {
		t.Fatalf("reconciler commit under a later epoch = %q, want applied, not a swallowed replay", status)
	}
	if v, _, _ := credentialSecretState(t, pool, c.ID); v != 3 {
		t.Fatalf("secret_version = %d, want 3 after two genuine commits", v)
	}
	if epoch, from, to := syncReceipt(t, pool, c.ID, "attempt-a"); epoch != 2 || from != 2 || to != 3 {
		t.Fatalf("receipt = (epoch %d, %d→%d), want it moved to the reconciler's epoch 2 and 2→3", epoch, from, to)
	}
	plain, err := svc.UseCredential(ctx, c.ID, "verify-sync")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, reconcilerBytes) {
		t.Fatalf("stored secret = %q, want the reconciler's bytes", plain)
	}

	// The superseded worker's retry is still fenced, receipt or not.
	status, err = svc.CommitCredentialSync(ctx, c.ID, "attempt-a", lease.Epoch, lease.SecretVersion, workerBytes)
	if err != nil {
		t.Fatal(err)
	}
	if status != assets.SyncFenced {
		t.Fatalf("superseded worker's retry = %q, want fenced", status)
	}
	if v, _, _ := credentialSecretState(t, pool, c.ID); v != 3 {
		t.Fatalf("secret_version = %d after the fenced retry, want the store untouched at 3", v)
	}
	if epoch, _, _ := syncReceipt(t, pool, c.ID, "attempt-a"); epoch != 2 {
		t.Fatalf("receipt epoch = %d after the fenced retry, want it still the reconciler's 2", epoch)
	}
}

// Scenario 5 (spec §2.6): a stale run never overwrites a fresh
// operator-supplied credential.
func TestCommitCredentialSync_OperatorRotationWinsTheCAS(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	lease, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	operator := authJSON(t, "rt-operator", 0)
	if _, err := svc.RotateCredentialSecret(ctx, admin, c.ID, operator); err != nil {
		t.Fatal(err)
	}

	status, err := svc.CommitCredentialSync(ctx, c.ID, "attempt-a", lease.Epoch, lease.SecretVersion,
		authJSON(t, "rt-run", 0))
	if err != nil {
		t.Fatal(err)
	}
	if status != assets.SyncCASLost {
		t.Fatalf("commit expecting the pre-rotation version = %q, want cas_lost", status)
	}
	if v, _, _ := credentialSecretState(t, pool, c.ID); v != 2 {
		t.Fatalf("secret_version = %d, want the operator's 2 with no further bump", v)
	}
	plain, err := svc.UseCredential(ctx, c.ID, "verify-rotation")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain, operator) {
		t.Fatalf("stored secret = %q, want the operator's auth.json", plain)
	}
	if n := auditCount(t, pool, "credential.sync_discarded"); n != 1 {
		t.Fatalf("credential.sync_discarded audits = %d, want 1", n)
	}
	if reason := auditDetail(t, pool, "credential.sync_discarded", "reason"); reason != "cas_lost" {
		t.Fatalf("discard reason = %q, want cas_lost", reason)
	}
	if n := receiptCount(t, pool, c.ID); n != 0 {
		t.Fatalf("sync receipts = %d after a discarded commit, want 0", n)
	}
}

// Scenario 6 (spec §2.1): expiry alone never breaks single-flight — the old
// container may still be alive and refreshing, so only a concluded
// reconciliation releases.
func TestAcquireCredentialLease_ExpiryAloneNeverReleases(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	if _, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a"); err != nil {
		t.Fatal(err)
	}
	backdateLease(t, pool, c.ID, 30*24*time.Hour)

	if _, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-b"); !errors.Is(err, assets.ErrLeaseBusy) {
		t.Fatalf("acquire against a long-expired lease = %v, want ErrLeaseBusy", err)
	}
	if _, _, _, held := leaseRow(t, pool, c.ID); !held {
		t.Fatal("a stale lease must survive a competing acquire")
	}
}

// Scenario 7 (spec §2.1, §2.3): release is epoch-matched, and the lease row
// is the recovery authority a reconciler looks up by attempt.
func TestReleaseCredentialLease_EpochMatchedAndLookupByAttempt(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	lease, _, err := svc.AcquireCredentialLease(ctx, c.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	found, ok, err := svc.CredentialLeaseByAttempt(ctx, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.CredentialID != c.ID || found.Epoch != lease.Epoch {
		t.Fatalf("lookup by attempt = (%+v, %v), want the held lease", found, ok)
	}

	// The taken-over worker relinquishes: its release is a silent no-op, not
	// an error it would report as a failure and not a lease it can destroy.
	if _, err := svc.TakeoverCredentialLease(ctx, c.ID, "attempt-a"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ReleaseCredentialLease(ctx, c.ID, "attempt-a", lease.Epoch); err != nil {
		t.Fatalf("stale-epoch release = %v, want a silent no-op", err)
	}
	if _, _, _, held := leaseRow(t, pool, c.ID); !held {
		t.Fatal("a stale-epoch release must leave the lease intact")
	}
	if n := auditCount(t, pool, "credential.lease_released"); n != 0 {
		t.Fatalf("credential.lease_released audits = %d after a no-op, want 0", n)
	}

	if err := svc.ReleaseCredentialLease(ctx, c.ID, "attempt-a", lease.Epoch+1); err != nil {
		t.Fatal(err)
	}
	if _, _, _, held := leaseRow(t, pool, c.ID); held {
		t.Fatal("a current-epoch release must delete the lease")
	}
	if _, ok, err := svc.CredentialLeaseByAttempt(ctx, "attempt-a"); err != nil || ok {
		t.Fatalf("lookup after release = (%v, %v), want absence", ok, err)
	}
	if n := auditCount(t, pool, "credential.lease_released"); n != 1 {
		t.Fatalf("credential.lease_released audits = %d, want 1", n)
	}
}

// Scenario 8 (spec §2 "exclusive custody transfer"): a refreshed token that
// now belongs to another registered credential is discarded by a pre-check
// inside the committed transaction. Letting the partial unique index raise
// 23505 instead would abort the transaction and take the discard audit with
// it — so the audit row existing is the proof this is a pre-check.
func TestCommitCredentialSync_FingerprintCollisionDiscards(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	a, _ := seedAuthFile(t, svc, admin, "codex-a", "rt-a")
	b, _ := seedAuthFile(t, svc, admin, "codex-b", "rt-b")

	lease, _, err := svc.AcquireCredentialLease(ctx, a.ID, "attempt-a")
	if err != nil {
		t.Fatal(err)
	}
	before := credCiphertext(t, pool, a.ID)

	status, err := svc.CommitCredentialSync(ctx, a.ID, "attempt-a", lease.Epoch, lease.SecretVersion,
		authJSON(t, "rt-b", 0))
	if err != nil {
		t.Fatalf("collision must be a status, not an error: %v", err)
	}
	if status != assets.SyncCASLost {
		t.Fatalf("commit onto another credential's refresh token = %q, want cas_lost", status)
	}
	if n := auditCount(t, pool, "credential.sync_discarded"); n != 1 {
		t.Fatalf("credential.sync_discarded audits = %d, want 1 committed alongside the discard", n)
	}
	if reason := auditDetail(t, pool, "credential.sync_discarded", "reason"); reason != "fingerprint_conflict" {
		t.Fatalf("discard reason = %q, want fingerprint_conflict", reason)
	}
	if v, _, _ := credentialSecretState(t, pool, a.ID); v != 1 {
		t.Fatalf("secret_version = %d after a discarded commit, want 1", v)
	}
	if !bytes.Equal(credCiphertext(t, pool, a.ID), before) {
		t.Fatal("a discarded commit must not rewrite the ciphertext")
	}
	if _, fp, _ := credentialSecretState(t, pool, b.ID); fp == nil {
		t.Fatal("the other credential must keep its fingerprint")
	}

	// The attempt still has to be able to conclude.
	if err := svc.ReleaseCredentialLease(ctx, a.ID, "attempt-a", lease.Epoch); err != nil {
		t.Fatal(err)
	}
	if _, _, _, held := leaseRow(t, pool, a.ID); held {
		t.Fatal("the lease must remain releasable after a discarded commit")
	}
}

// RecordCredentialQuarantine is the audit-only path for an identity-changed
// read-back (spec §2.5): the stored credential is preserved untouched.
func TestRecordCredentialQuarantine_AuditsAndPreservesTheStore(t *testing.T) {
	ctx := context.Background()
	svc, pool, _ := newTestService(t)
	admin := seedActor(t, pool, "root@example.com", authn.RoleAdmin)
	c, _ := seedAuthFile(t, svc, admin, "codex-sub", "rt-1")

	before := credCiphertext(t, pool, c.ID)
	if err := svc.RecordCredentialQuarantine(ctx, c.ID, "attempt-a"); err != nil {
		t.Fatal(err)
	}
	if n := auditCount(t, pool, "credential.sync_quarantined"); n != 1 {
		t.Fatalf("credential.sync_quarantined audits = %d, want 1", n)
	}
	if attempt := auditDetail(t, pool, "credential.sync_quarantined", "attempt"); attempt != "attempt-a" {
		t.Fatalf("quarantine audit attempt = %q, want attempt-a", attempt)
	}
	if v, _, _ := credentialSecretState(t, pool, c.ID); v != 1 {
		t.Fatalf("secret_version = %d after a quarantine, want the store untouched", v)
	}
	if !bytes.Equal(credCiphertext(t, pool, c.ID), before) {
		t.Fatal("a quarantine must not rewrite the ciphertext")
	}
}

func TestCredentialLease_UnknownCredential(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newTestService(t)

	if _, _, err := svc.AcquireCredentialLease(ctx, uuid.New(), "attempt-a"); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("acquire against an unknown credential = %v, want not found", err)
	}
	if _, err := svc.TakeoverCredentialLease(ctx, uuid.New(), "attempt-a"); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("takeover of a lease nobody holds = %v, want not found", err)
	}
}
