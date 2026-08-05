package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
)

const (
	// authFileReadLimit bounds the read-back of a file an agent could write:
	// a real auth.json is a few kilobytes, and anything past this is not a
	// credential.
	authFileReadLimit = 128 << 10

	// credentialConcludeTimeout bounds the whole conclusion — read-back,
	// commit, cleanup, release. It runs on a context of its own: the
	// attempt's may be exactly the one that just expired, and a lease nobody
	// released blocks every later run of every phase bound to that credential.
	credentialConcludeTimeout = 30 * time.Second
)

// syncConclusion is what concluding the credential obligation left behind.
// Status vocabulary (pinned): "clean" (nothing to sync), "synced" (committed),
// "discarded" (CAS lost to an operator rotation), "unscannable" (read-back
// unusable — Task 11 withholds this attempt's logs and artifacts),
// "quarantined" (identity mismatch), "fenced" (a reconciler owns the lease now).
type syncConclusion struct {
	Status             string
	RefreshedSensitive [][]byte      // token fields recovered from the read-back file
	Outcome            *runs.Outcome // set only on quarantine: permanent credential_quarantined
}

// concludeCredential is the single choke point where an attempt's credential
// obligation ends (spec §2.5, §2.6). Every exit path — success, failure,
// enforcement kill, reconciliation, adoption — funnels through here; nothing
// else releases the lease or touches the auth directory.
//
// It runs under its own bounded, UNCANCELLED context: the most important
// caller is exactly the one whose ctx is already dead (the wall-deadline
// exit — orchestrator.go passes a deadline-bound execCtx into Execute), and
// a conclusion that fails on `context deadline exceeded` would strand every
// deadline-exit lease for the janitor. Same pattern as teardown.
func (r *Runner) concludeCredential(ctx context.Context, ref runs.AttemptRef, m attemptManifest, credentialID uuid.UUID) syncConclusion {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), credentialConcludeTimeout)
	defer cancel()
	release := func() {
		if err := r.leaser.Release(ctx, credentialID, ref.String(), m.Epoch); err != nil {
			r.log.Warn("runner.lease_release", "attempt", ref.String(), "error", err)
		}
	}
	if m.AuthDir == "" { // env-delivered credential: nothing mutable to guard
		release()
		return syncConclusion{Status: "clean"}
	}
	raw, ok := r.readAuthFile(ref)
	if !ok {
		release() // store preserved; the credential may be dead (§2.7 accepted loss window)
		return syncConclusion{Status: "unscannable"}
	}
	sens, _ := codexSensitive(raw) // best-effort even before identity: these bytes must never publish
	if bytes.Equal(raw, m.secretBytes) {
		// The CLI never wrote. Recovery paths that lack the materialized
		// bytes (the manifest never serializes them) skip this shortcut and
		// let the CAS settle it — already_applied and cas_lost cover them.
		if !r.removeAuthDirFenced(ctx, ref, credentialID, m.Epoch) {
			return syncConclusion{Status: "fenced", RefreshedSensitive: sens}
		}
		release()
		return syncConclusion{Status: "clean", RefreshedSensitive: sens}
	}
	id, err := codexIdentity(raw)
	if err != nil {
		// Identity unreadable = no complete scan: same terminal shape as any
		// other unscannable read-back, and the store stays as it was.
		release()
		return syncConclusion{Status: "unscannable", RefreshedSensitive: sens}
	}
	if id != m.Identity {
		// Agent-authored substitution (spec §2.5): preserve the store, type
		// the outcome, audit — never commit.
		if err := r.leaser.RecordQuarantine(ctx, credentialID, ref.String()); err != nil {
			r.log.Warn("runner.record_quarantine", "attempt", ref.String(),
				"credential", credentialID.String(), "error", err)
		}
		release()
		return syncConclusion{Status: "quarantined", RefreshedSensitive: sens, Outcome: &runs.Outcome{
			Class: runs.OutcomePermanent, Code: runs.CodeCredentialQuarantined,
			Message: fmt.Sprintf("credential %s: read-back identity mismatch; stored credential preserved", credentialID),
		}}
	}
	status, err := r.leaser.CommitSync(ctx, credentialID, ref.String(), m.Epoch, m.SecretVersion, raw)
	switch {
	case xerrors.ClassOf(err) == xerrors.ClassInvalid && err != nil:
		// Permanent: retrying the same bytes can never succeed, so the lease
		// is handed back — the no-release path is ONLY for fencing and infra
		// uncertainty. Store preserved, evidence retained.
		release()
		return syncConclusion{Status: "unscannable", RefreshedSensitive: sens}
	case err != nil:
		// Infra uncertainty: the commit may or may not have landed, so this
		// worker keeps its hands off — the lease stays held and the stale-
		// lease janitor retries under the same fence (already_applied makes
		// that retry idempotent).
		r.log.Warn("runner.credential_sync", "attempt", ref.String(),
			"credential", credentialID.String(), "error", err)
		return syncConclusion{Status: "fenced", RefreshedSensitive: sens}
	case status == SyncStatusFenced:
		return syncConclusion{Status: "fenced", RefreshedSensitive: sens}
	}
	r.removeAuthDirFenced(ctx, ref, credentialID, m.Epoch)
	release()
	if status == SyncStatusCASLost {
		// An operator rotated mid-attempt: the stale snapshot dies quietly
		// (the discard is audited assets-side).
		return syncConclusion{Status: "discarded", RefreshedSensitive: sens}
	}
	return syncConclusion{Status: "synced", RefreshedSensitive: sens}
}

// readAuthFile is the read gate over the one agent-writable file on the host:
// a bounded read of a regular file is the only read there is (the Lstat comes
// first because opening anything else — a fifo, say — could block forever),
// and only the FULL pinned commit shape passes. The shape check matters
// beyond hygiene: assets' validateSecret refuses an auth.json without a
// refresh token, and a commit that is doomed to a permanent validation error
// must never be attempted — the janitor would deterministically retry the
// same invalid file forever, holding the lease as an eternal credential_busy.
//
// The file name and shape are the Codex driver's: the auth-directory
// machinery has exactly one materialized file kind today, and this gate
// parses that kind's schema.
func (r *Runner) readAuthFile(ref runs.AttemptRef) ([]byte, bool) {
	path := filepath.Join(r.authDir(ref), drivers["codex"].authMount.file)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > authFileReadLimit {
		return nil, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	f, err := parseCodexAuthFile(raw)
	if err != nil || f.Tokens.RefreshToken == "" {
		return nil, false
	}
	return raw, true
}
