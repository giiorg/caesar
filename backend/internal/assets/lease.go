package assets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/assets/store"
	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/httpx"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/platform/secrets"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// CredentialLease is the single-flight grant over a mutable credential
// (spec §2.1). Epoch is the takeover fence: reconciliation advances it
// before it touches the container, so a paused worker and the reconciler
// that superseded it — the same phase run, indistinguishable by identity —
// can still be told apart. SecretVersion is the version the lease granted;
// the sync-back CAS is compared against it.
type CredentialLease struct {
	CredentialID  uuid.UUID
	AttemptRef    string
	Epoch         int64
	SecretVersion int64
	AcquiredAt    time.Time
	HeartbeatAt   time.Time
}

func leaseFromRow(r store.CredentialLease) CredentialLease {
	return CredentialLease{
		CredentialID: r.CredentialID, AttemptRef: r.AttemptRef,
		Epoch: r.Epoch, SecretVersion: r.SecretVersion,
		AcquiredAt: r.AcquiredAt, HeartbeatAt: r.HeartbeatAt,
	}
}

// ErrLeaseBusy reports that the caller may not have the lease. It rides a
// ClassConflict error; the runner turns it into the zero-charge
// credential_busy outcome rather than a failure.
var ErrLeaseBusy = errors.New("assets: credential lease held")

// lease0Epoch is the epoch a fresh lease starts at (the schema default);
// anything above it means a takeover happened.
const lease0Epoch = 1

// SyncStatus is the outcome of a sync-back commit. None of the four is an
// error: three of them mean "this attempt's bytes were not stored, and that
// is correct" — the caller releases its lease either way.
type SyncStatus string

const (
	SyncApplied        SyncStatus = "applied"
	SyncAlreadyApplied SyncStatus = "already_applied"
	SyncCASLost        SyncStatus = "cas_lost"
	SyncFenced         SyncStatus = "fenced"
)

// AcquireCredentialLease acquires the single-flight lease AND snapshots the
// credential it grants in one transaction: the returned plaintext is the
// bytes of exactly the secret_version recorded on the lease row (spec §3 —
// the acquired version must correspond to the materialized credential). The
// runner materializes THESE bytes; it never performs a separate open for a
// leased credential, so an operator rotation between acquire and delivery
// cannot make sync-back's CAS discard a legitimate refresh.
//
// Acquisition is idempotent for the attempt that already holds the lease at
// its original epoch: a retried attempt re-binds to the current version
// rather than deadlocking against itself. Everything else gets ErrLeaseBusy
// — another attempt; a lease whose heartbeat expired long ago, because the
// old container may still be alive and refreshing (spec §2.1: only
// conclusion releases); and the attempt's OWN lease once a takeover has
// advanced the epoch, because custody then belongs to recovery. Reconcilers
// reach a taken-over lease through TakeoverCredentialLease, never here, so
// no legitimate path needs the re-acquire past epoch 1.
func (s *Service) AcquireCredentialLease(ctx context.Context, credentialID uuid.UUID, attemptRef string) (CredentialLease, []byte, error) {
	var (
		lease CredentialLease
		env   secrets.Envelope
	)
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// Lock the credential row first — the aggregate's lock order — and
		// capture the envelope, the version the lease is about to grant, and
		// the freshness stamp from that one atomic read.
		cred, err := q.AcquireCredentialRow(ctx, credentialID)
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no such credential")
		}
		if err != nil {
			return fmt.Errorf("assets: acquire credential row: %w", err)
		}
		env = secrets.Envelope{KeyID: cred.KeyID, WrappedDEK: cred.WrappedDek, Ciphertext: cred.Ciphertext}

		row, err := q.AcquireLease(ctx, store.AcquireLeaseParams{
			CredentialID: credentialID, AttemptRef: attemptRef, SecretVersion: cred.SecretVersion,
		})
		if constraint, dup := uniqueViolation(err); dup && constraint == leaseAttemptIndex {
			return xerrors.E(xerrors.ClassConflict, "attempt already holds a lease on another credential")
		}
		reacquired := false
		switch {
		case errors.Is(err, pgx.ErrNoRows): // held — by us (idempotent) or by another attempt (busy)
			held, getErr := q.GetLease(ctx, credentialID)
			if getErr != nil {
				return fmt.Errorf("assets: get credential lease: %w", getErr)
			}
			if held.AttemptRef != attemptRef {
				return xerrors.Wrap(xerrors.ClassConflict, "credential lease held by another attempt", ErrLeaseBusy)
			}
			if held.Epoch > lease0Epoch {
				// Our own lease, but a takeover moved custody to recovery.
				// Acquiring it back would let the superseded worker resume
				// under a lease the reconciler is concluding.
				return xerrors.Wrap(xerrors.ClassConflict, "credential lease taken over by recovery", ErrLeaseBusy)
			}
			// The store may have rotated since the first acquire, and the
			// snapshot below is what materializes — so lease.secret_version
			// must be re-bound to match it. Safe: the caller just proved it
			// is the holder, inside the same tx as the row read.
			rebound, rebindErr := q.RebindLeaseVersion(ctx, store.RebindLeaseVersionParams{
				CredentialID: credentialID, AttemptRef: attemptRef, SecretVersion: cred.SecretVersion,
			})
			if rebindErr != nil {
				return fmt.Errorf("assets: rebind credential lease version: %w", rebindErr)
			}
			row, reacquired = rebound, true
		case err != nil:
			return fmt.Errorf("assets: acquire credential lease: %w", err)
		}

		lease = leaseFromRow(row)
		detail := map[string]any{"attempt": attemptRef, "epoch": lease.Epoch, "secret_version": lease.SecretVersion}
		if reacquired {
			detail["reacquired"] = true
		}
		// Every successful acquire hands back plaintext, so every successful
		// acquire is the audited use for that open — the re-acquire included.
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.lease_acquired", SubjectKind: "credential", SubjectID: credentialID.String(),
			CorrelationID: httpx.CorrelationID(ctx), Detail: detail,
		})
	})
	if err != nil {
		return CredentialLease{}, nil, err
	}
	// Unseal outside the tx — the UseCredential precedent. The
	// lease-acquired audit row IS the audited use for this open.
	plain, err := s.keys.Open(env, []byte(credentialID.String()))
	if err != nil {
		return CredentialLease{}, nil, fmt.Errorf("assets: open credential %s: %w", credentialID, err)
	}
	return lease, plain, nil
}

// HeartbeatCredentialLease keeps the lease fresh for the staleness sweep.
// A heartbeat that matches no row means the caller no longer owns the lease
// — taken over, or released out from under it — and the conflict tells the
// worker its attempt now belongs to the reconciler. Deliberately unaudited:
// a row every heartbeat interval per attempt would bury the audit log, and
// the acquire/takeover/release triple already records every ownership change.
func (s *Service) HeartbeatCredentialLease(ctx context.Context, credentialID uuid.UUID, attemptRef string, epoch int64) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		n, err := store.New(tx).HeartbeatLease(ctx, store.HeartbeatLeaseParams{
			CredentialID: credentialID, AttemptRef: attemptRef, Epoch: epoch,
		})
		if err != nil {
			return fmt.Errorf("assets: heartbeat credential lease: %w", err)
		}
		if n == 0 {
			return xerrors.E(xerrors.ClassConflict, "credential lease fenced")
		}
		return nil
	})
}

// TakeoverCredentialLease advances the epoch, fencing the previous owner
// out of sync-back and cleanup (spec §2.1, §2.4). Reconciliation calls it
// BEFORE killing or reading the container, so a worker that resumes from a
// pause finds itself superseded.
func (s *Service) TakeoverCredentialLease(ctx context.Context, credentialID uuid.UUID, attemptRef string) (CredentialLease, error) {
	var lease CredentialLease
	err := pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// The credential row lock first, as everywhere in this aggregate —
		// and load-bearing here for the reason ReleaseCredentialLease spells
		// out: an acquire decides across several statements while holding it,
		// and a takeover landing mid-decision slips past an epoch check that
		// has already passed, handing the worker it supersedes the very epoch
		// this call is claiming.
		if _, err := q.LockCredential(ctx, credentialID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return xerrors.E(xerrors.ClassNotFound, "no such credential")
			}
			return fmt.Errorf("assets: lock credential: %w", err)
		}
		row, err := q.TakeoverLease(ctx, store.TakeoverLeaseParams{
			CredentialID: credentialID, AttemptRef: attemptRef,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return xerrors.E(xerrors.ClassNotFound, "no credential lease for this attempt")
		}
		if err != nil {
			return fmt.Errorf("assets: take over credential lease: %w", err)
		}
		lease = leaseFromRow(row)
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.lease_takeover", SubjectKind: "credential", SubjectID: credentialID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"attempt": attemptRef, "epoch": lease.Epoch},
		})
	})
	if err != nil {
		return CredentialLease{}, err
	}
	return lease, nil
}

// CommitCredentialSync stores an attempt's refreshed credential under the
// double guard of spec §2.6: the current lease epoch (this attempt is still
// the owner) and the version it materialized (no operator rotation since).
// The receipt keyed by (credential, attempt) makes the commit-then-crash
// retry recognizable as already applied — but only a receipt from the SAME
// epoch does, because a reconciler that took over commits under the same
// attempt ref and its fresher bytes must not be mistaken for a replay.
//
// Only a malformed secret is an error — it is permanent, so the caller
// releases instead of retrying. Every other non-applied outcome is a status:
// the attempt's bytes were correctly not stored.
func (s *Service) CommitCredentialSync(
	ctx context.Context, credentialID uuid.UUID, attemptRef string, epoch, expectVersion int64, newSecret []byte,
) (SyncStatus, error) {
	// Validate, fingerprint, and seal before the transaction — the
	// RotateCredentialSecret precedent, and crypto stays outside
	// transactions by package convention. Kind is immutable, so reading it
	// on the pool is race-free.
	kind, err := s.credentialKind(ctx, credentialID)
	if err != nil {
		return "", err
	}
	if err := validateSecret(kind, newSecret); err != nil {
		return "", err
	}
	fingerprint, err := credentialFingerprint(kind, newSecret)
	if err != nil {
		return "", err
	}
	env, err := s.keys.Seal(newSecret, []byte(credentialID.String()))
	if err != nil {
		return "", fmt.Errorf("assets: seal credential: %w", err)
	}

	status := SyncApplied
	err = pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// The credential row lock comes first, as everywhere in this
		// aggregate: it pins the lock order and proves the row still exists.
		if _, err := q.LockCredential(ctx, credentialID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return xerrors.E(xerrors.ClassNotFound, "no such credential")
			}
			return fmt.Errorf("assets: lock credential: %w", err)
		}

		// Reading the receipt under that lock is what makes two racing
		// retries of one commit serialize into applied + already_applied
		// instead of applied + cas_lost.
		receipt, err := q.GetSyncReceipt(ctx, store.GetSyncReceiptParams{CredentialID: credentialID, AttemptRef: attemptRef})
		switch {
		case err == nil && receipt.Epoch == epoch:
			// This attempt already committed at this epoch: the
			// commit-then-crash retry, recognizable and harmless.
			status = SyncAlreadyApplied
			return nil
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("assets: get sync receipt: %w", err)
		}
		// A receipt from a different epoch belongs to the owner this caller
		// superseded (or to the owner that superseded this caller); fall
		// through to the fence and the CAS, which decide correctly either way.

		// Locking the lease row closes the window in which a takeover could
		// land between the fence check and the write.
		lease, err := q.GetLeaseForUpdate(ctx, credentialID)
		held := true
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			held = false
		case err != nil:
			return fmt.Errorf("assets: lock credential lease: %w", err)
		}
		// Released, reassigned, superseded: all mean this caller is no
		// longer the holder.
		if !held || lease.AttemptRef != attemptRef || lease.Epoch != epoch {
			status = SyncFenced
			return nil
		}

		if fingerprint.Valid {
			// A refreshed token that now belongs to another registered
			// credential is discarded here rather than by the unique index:
			// a 23505 would ABORT the transaction, and the discard audit
			// could not be written after that without savepoints. The
			// residual race — a concurrent commit onto the same fingerprint
			// — still surfaces below as a conflict, and the retry meets this
			// pre-check next round.
			collides, checkErr := q.CredentialFingerprintHeldByOther(ctx, store.CredentialFingerprintHeldByOtherParams{
				ID: credentialID, Fingerprint: fingerprint,
			})
			if checkErr != nil {
				return fmt.Errorf("assets: sync fingerprint pre-check: %w", checkErr)
			}
			if collides {
				status = SyncCASLost
				return s.recordSyncDiscarded(ctx, tx, credentialID, attemptRef, epoch, expectVersion, "fingerprint_conflict")
			}
		}

		n, err := q.UpdateCredentialSecretCAS(ctx, store.UpdateCredentialSecretCASParams{
			ID: credentialID, KeyID: env.KeyID, WrappedDek: env.WrappedDEK, Ciphertext: env.Ciphertext,
			Fingerprint: fingerprint, SecretVersion: expectVersion,
		})
		if constraint, dup := uniqueViolation(err); dup && constraint == fingerprintIndex {
			return errDuplicateRefreshToken()
		}
		if err != nil {
			return fmt.Errorf("assets: sync credential secret: %w", err)
		}
		if n == 0 {
			// The operator rotated mid-attempt: a stale run never overwrites
			// a fresh operator-supplied credential.
			status = SyncCASLost
			return s.recordSyncDiscarded(ctx, tx, credentialID, attemptRef, epoch, expectVersion, "cas_lost")
		}
		if err := q.UpsertSyncReceipt(ctx, store.UpsertSyncReceiptParams{
			CredentialID: credentialID, AttemptRef: attemptRef, Epoch: epoch,
			FromVersion: expectVersion, ToVersion: expectVersion + 1,
		}); err != nil {
			return fmt.Errorf("assets: record sync receipt: %w", err)
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.synced", SubjectKind: "credential", SubjectID: credentialID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail: map[string]any{
				"attempt": attemptRef, "epoch": epoch, "key_id": env.KeyID,
				"from_version": expectVersion, "to_version": expectVersion + 1,
			},
		})
	})
	if err != nil {
		return "", err
	}
	return status, nil
}

// recordSyncDiscarded audits a sync-back the store refused, in the same
// transaction that decided to refuse it.
func (s *Service) recordSyncDiscarded(
	ctx context.Context, tx pgx.Tx, credentialID uuid.UUID, attemptRef string, epoch, expectVersion int64, reason string,
) error {
	return s.audit.Record(ctx, tx, audit.Entry{
		Action: "credential.sync_discarded", SubjectKind: "credential", SubjectID: credentialID.String(),
		CorrelationID: httpx.CorrelationID(ctx),
		Detail: map[string]any{
			"attempt": attemptRef, "epoch": epoch, "expected_version": expectVersion, "reason": reason,
		},
	})
}

// ReleaseCredentialLease concludes the attempt's custody. An epoch mismatch
// is a silent no-op: the caller has been taken over, and relinquishing is
// exactly what it should do — reporting a failure it cannot act on would
// only turn a healthy handover into a spurious error.
func (s *Service) ReleaseCredentialLease(ctx context.Context, credentialID uuid.UUID, attemptRef string, epoch int64) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		// The credential row lock opens every write in this aggregate, and
		// this is the write that proved why. AcquireCredentialLease decides
		// across three statements under that lock — held? by whom? re-bind —
		// and a release that skipped the lock could delete the lease between
		// two of them. The acquirer then read nothing where it had just been
		// told somebody was there, and pgx.ErrNoRows left the aggregate as an
		// unclassified, PERMANENT error: a run killed by the good news that
		// the credential it wanted had come free.
		if _, err := q.LockCredential(ctx, credentialID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// No credential, therefore no lease — the foreign key is ON
				// DELETE RESTRICT. Nothing to conclude, and a conclusion that
				// finds nothing to do has always been this method's no-op.
				return nil
			}
			return fmt.Errorf("assets: lock credential: %w", err)
		}
		n, err := q.ReleaseLease(ctx, store.ReleaseLeaseParams{
			CredentialID: credentialID, AttemptRef: attemptRef, Epoch: epoch,
		})
		if err != nil {
			return fmt.Errorf("assets: release credential lease: %w", err)
		}
		if n == 0 {
			return nil
		}
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.lease_released", SubjectKind: "credential", SubjectID: credentialID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"attempt": attemptRef, "epoch": epoch},
		})
	})
}

// CredentialLeaseByAttempt is the recovery-authority lookup (spec §2.3):
// reconciliation consults the lease row even when no manifest and no
// container exist, so a crash between lease commit and manifest durability
// resolves to a release instead of a credential_busy loop against a lease
// nobody can conclude.
func (s *Service) CredentialLeaseByAttempt(ctx context.Context, attemptRef string) (CredentialLease, bool, error) {
	row, err := store.New(s.pool).GetLeaseByAttempt(ctx, attemptRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return CredentialLease{}, false, nil
	}
	if err != nil {
		return CredentialLease{}, false, fmt.Errorf("assets: get credential lease by attempt: %w", err)
	}
	return leaseFromRow(row), true, nil
}

// ListStaleCredentialLeases surfaces leases whose owner stopped heartbeating
// (spec §2.8): expiry alone never breaks single-flight — these are handed to
// reconciliation, never to a new acquirer.
func (s *Service) ListStaleCredentialLeases(ctx context.Context, olderThan time.Duration) ([]CredentialLease, error) {
	rows, err := store.New(s.pool).ListStaleLeases(ctx, olderThan.Seconds())
	if err != nil {
		return nil, fmt.Errorf("assets: list stale credential leases: %w", err)
	}
	out := make([]CredentialLease, 0, len(rows))
	for _, row := range rows {
		out = append(out, leaseFromRow(row))
	}
	return out, nil
}

// RecordCredentialQuarantine audits a read-back whose account identity no
// longer matches the credential caesar materialized (spec §2.5) — agent-
// authored content, not a CLI refresh. Audit-only by design: the stored
// credential is preserved untouched, which is the whole point.
func (s *Service) RecordCredentialQuarantine(ctx context.Context, credentialID uuid.UUID, attemptRef string) error {
	return pg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		return s.audit.Record(ctx, tx, audit.Entry{
			Action: "credential.sync_quarantined", SubjectKind: "credential", SubjectID: credentialID.String(),
			CorrelationID: httpx.CorrelationID(ctx),
			Detail:        map[string]any{"attempt": attemptRef},
		})
	})
}
