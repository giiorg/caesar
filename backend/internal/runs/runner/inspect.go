package runner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// Inspect reconciles one attempt against the daemon's own truth (spec §3).
// It is called for an attempt whose worker is gone — the lease expired, or a
// process died between the pipeline finishing and its result being written —
// so the only witness left is the agent container named after the ref.
//
// The answer is adopt-or-absent and never half: an attempt is adopted only if
// its whole result can be collected here, and every other path tears the
// remnants down and reports nothing, which the orchestrator concludes as a
// transient reconcile_lost and retries honestly.
//
// The credential obligation is settled on EVERY path, adopted or not, and in
// the pinned order: take custody first, stop the runtime, then settle (spec
// §2.8). A path that returned early without settling would strand the lease
// until the stale-lease janitor noticed, and every attempt behind it would meet
// a credential_busy in the meantime.
func (r *Runner) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	credID, lease, held, err := r.takeoverCredential(ctx, ref)
	if err != nil {
		// Custody could not be taken, so nothing here may touch the container
		// or the credential: a still-paused worker is not fenced yet, and
		// reading auth.json beside it could commit bytes it is still writing.
		// The orchestrator reads this as reconcile_lost and comes back.
		return runs.InspectResult{}, err
	}

	insp, err := r.engine.ContainerInspect(ctx, agentName(ref))
	if dockerapi.IsNotFound(err) {
		// Nothing to adopt and nothing to remove: either the attempt never
		// got as far as an agent, or a teardown already ran — including the
		// teardown of a worker this takeover has just fenced. The obligation
		// outlives all of that, so it still settles.
		r.settleCredential(ctx, ref, credID, lease, held)
		return runs.InspectResult{}, nil
	}
	if err != nil {
		// The daemon could not be asked, so nothing is known — and tearing
		// down what might still be a live attempt on the strength of a failed
		// inspect is the one move that could destroy work. The lease stays
		// held (this takeover already fenced the old owner); the next
		// reconciliation settles it.
		return runs.InspectResult{}, transientf(runs.CodeInternalError,
			"inspect %s: %v", agentName(ref), err)
	}

	ourKill := false
	if insp.State.Status == "running" {
		// A container still running has no lease behind it any more. It is
		// killed rather than waited on: whatever it is doing, no worker is
		// listening for the answer.
		insp, ourKill = r.killThenInspect(ctx, ref, insp)
	}

	// The attempt directory is what collection reads. Execute removes it as
	// the last thing it does, so a finished container beside a missing
	// directory is a worker that died after its own teardown — the work is
	// gone, and only the corpse is left to clear.
	//
	// A nonzero exit the harness reached on its own IS adoptable: collection
	// reads the markers off it, so a crash after a completed rate_limited or
	// auth_expired exit reports that outcome instead of degrading to
	// reconcile_lost — which would charge a cap hit to the node's failure
	// budget. A nonzero exit this reconciliation caused says nothing about the
	// harness: 137 is our own signal coming back, and the attempt never
	// finished at all.
	if insp.State.Status != "exited" || !r.attemptDirExists(ref) ||
		(ourKill && insp.State.ExitCode != 0) {
		// The kill above may not have landed — that is one of the reasons this
		// branch is reached — so the runtime is force-removed and the removal
		// is checked. Nothing is adopted here, so losing the corpse costs
		// nothing; settling over a container the daemon would not remove would
		// cost the credential, because that container may still be writing it.
		if err := r.stopAttemptRuntime(ctx, ref); err != nil {
			return runs.InspectResult{}, err
		}
		r.teardown(ctx, ref)
		r.settleCredential(ctx, ref, credID, lease, held)
		return runs.InspectResult{}, nil
	}
	// The container is exited, so nothing can write auth.json between the
	// read-back and the commit — the settlement's precondition, proved.
	rec := r.settleCredential(ctx, ref, credID, lease, held)
	return r.adopt(ctx, ref, insp, rec), nil
}

// ReconcileCredential settles an attempt's credential obligation and nothing
// else (runs.CredentialReconciler): the entry point for the janitor paths whose
// attempt already has an outcome — a run past its wall deadline, a lease whose
// owner stopped heartbeating. Deliberately no adoption: a completed attempt
// evaluated here could be persisted as a success the run is no longer entitled
// to (FinishRun carries no deadline predicate).
func (r *Runner) ReconcileCredential(ctx context.Context, ref runs.AttemptRef) error {
	credID, lease, held, err := r.takeoverCredential(ctx, ref)
	if err != nil {
		return err
	}
	// The force-remove IS the kill, and it does not return until the daemon
	// has done it. There is nothing to adopt here, so the exit code the corpse
	// carries is worth nothing and losing it is free.
	if err := r.stopAttemptRuntime(ctx, ref); err != nil {
		// Refusing to settle over a runtime that may still be running is the
		// whole point: the caller retries, and until then the lease stays held
		// under this reconciler's own epoch.
		return err
	}
	r.settleCredential(ctx, ref, credID, lease, held)
	return nil
}

// stopAttemptRuntime removes the two containers that can still be executing an
// attempt's code, and reports whether it can promise they are gone. Already-
// gone is success; anything else is not, because the settlement that follows
// reads a file the agent may still be writing.
func (r *Runner) stopAttemptRuntime(ctx context.Context, ref runs.AttemptRef) error {
	for _, name := range []string{agentName(ref), gatewayName(ref)} {
		if err := r.engine.ContainerRemove(ctx, name, true); err != nil && !dockerapi.IsNotFound(err) {
			return transientf(runs.CodeInternalError, "remove %s: %v", name, err)
		}
	}
	return nil
}

// credentialReconciliation is everything settling the obligation learned —
// adoption (Task 11) rebuilds its redaction set from this, so it is captured
// BEFORE conclusion deletes the on-disk manifest or bumps the store version.
type credentialReconciliation struct {
	Manifest    attemptManifest // in-memory copy; zero-valued when absent
	HasManifest bool
	Stored      []byte // stored credential bytes, opened pre-commit; nil if open failed
	StoredOK    bool   // open succeeded AND at the version the manifest recorded
	Concluded   bool   // a lease existed and concludeCredential ran
	Conc        syncConclusion
}

// takeoverCredential is stage 1: fence the paused owner (spec §2.1, §2.3 —
// takeover FIRST, before that owner can race its own commit or cleanup).
// Fencing the WORKER is all it does: the epoch stops the old process's
// database writes, not the container's file writes — the runtime must be
// provably stopped by the caller before settleCredential may read auth.json.
//
// The lease row is the authority, not the manifest: an attempt that crashed
// between the acquire and the manifest write still resolves to its credential
// here, which is what keeps that lease from becoming an eternal credential_busy.
func (r *Runner) takeoverCredential(ctx context.Context, ref runs.AttemptRef) (uuid.UUID, Lease, bool, error) {
	if r.leaser == nil {
		return uuid.Nil, Lease{}, false, nil // no aggregate wired: no leased kinds run at all
	}
	credID, _, ok, err := r.leaser.LeaseByAttempt(ctx, ref.String())
	if err != nil {
		return uuid.Nil, Lease{}, false, transientf(runs.CodeInternalError,
			"look up credential lease for %s: %v", ref.String(), err)
	}
	if !ok {
		return uuid.Nil, Lease{}, false, nil
	}
	lease, err := r.leaser.Takeover(ctx, credID, ref.String())
	if leaseAlreadyConcluded(err) {
		return uuid.Nil, Lease{}, false, nil
	}
	if err != nil {
		return uuid.Nil, Lease{}, false, transientf(runs.CodeInternalError,
			"take over credential %s for %s: %v", credID, ref.String(), err)
	}
	r.log.Info("runner.credential_takeover", "attempt", ref.String(),
		"credential", credID.String(), "epoch", lease.Epoch)
	return credID, lease, true, nil
}

// leaseAlreadyConcluded reports the one takeover refusal that is not a
// failure: the lease row is gone, because the owner got its own conclusion in
// between the lookup and the takeover. There is nothing left to take custody
// of, and calling that an error would leave the deadline sweep retrying
// forever against an obligation that is already settled.
func leaseAlreadyConcluded(err error) bool {
	return err != nil && xerrors.ClassOf(err) == xerrors.ClassNotFound
}

// settleCredential is stage 3: capture the redaction inputs, then conclude.
// PRECONDITION (each caller's to prove, tested): the attempt's containers are
// gone or exited — a still-running agent could write auth.json between the
// read-back and the commit, and a released lease would let a second attempt
// acquire while the first still runs.
func (r *Runner) settleCredential(ctx context.Context, ref runs.AttemptRef,
	credentialID uuid.UUID, lease Lease, held bool,
) credentialReconciliation {
	var rec credentialReconciliation
	// The manifest read is NOT gated on the lease: static kinds
	// (subscription_token) write one with no lease behind it, and adoption
	// needs it either way.
	rec.Manifest, rec.HasManifest = r.readManifestForRecovery(ref)
	if rec.HasManifest {
		// Open the stored credential BEFORE any CommitSync bumps the version:
		// these are the only recoverable copy of the initially-materialized
		// bytes, and adoption may not publish logs it cannot scan for them.
		if id, err := uuid.Parse(rec.Manifest.CredentialID); err == nil && r.creds != nil {
			// The manifest's version is what was DELIVERED (every kind records
			// it); the store must still hold it, or the delivered bytes are
			// unrecoverable and adoption must withhold. `!held` is no
			// exemption: a rotated subscription_token's old value entered the
			// crashed attempt's env just the same.
			if b, v, oerr := r.creds.OpenCredential(ctx, id, "reconcile:"+ref.String()); oerr == nil {
				rec.Stored, rec.StoredOK = b, v == rec.Manifest.SecretVersion
			} else {
				r.log.Warn("runner.reconcile.open_credential", "attempt", ref.String(), "error", oerr)
			}
		}
	}
	if !held {
		return rec // no lease: nothing to conclude (auth-dir leftovers age into the orphan sweep)
	}
	m := rec.Manifest // conclusion works on a copy; a missing manifest concludes as "clean"
	// (crash between lease commit and manifest write, spec §2.3 — the lease
	// row is the recovery authority; release, never loop on credential_busy)
	m.Epoch = lease.Epoch                 // conclude under the taken-over epoch
	m.SecretVersion = lease.SecretVersion // the version the lease actually granted
	rec.Conc = r.concludeCredential(ctx, ref, m, credentialID)
	rec.Concluded = true
	r.log.Info("runner.credential_reconciled", "attempt", ref.String(), "status", rec.Conc.Status)
	return rec
}

// readManifestForRecovery reads the attempt manifest the way every recovery
// path wants it: absent is the documented gap (a crash before the write), and
// anything else is worth a line in the log but still reads as absent — a
// manifest this build cannot parse is not one it may act on.
func (r *Runner) readManifestForRecovery(ref runs.AttemptRef) (attemptManifest, bool) {
	m, err := r.readManifest(ref)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Warn("runner.reconcile.manifest", "attempt", ref.String(), "error", err)
		}
		return attemptManifest{}, false
	}
	return m, true
}

// attemptDirExists reports whether the attempt still has a working directory
// to collect from. Anything other than a readable directory reads as absent:
// a workspace this process cannot stat is one it cannot extract a patch from
// either.
func (r *Runner) attemptDirExists(ref runs.AttemptRef) bool {
	fi, err := os.Stat(r.attemptDir(ref))
	return err == nil && fi.IsDir()
}

// killThenInspect stops a container reconciliation found still running and
// reports what it left behind, plus whether the kill actually landed — the
// caller reads an exit code differently depending on whose signal produced
// it. A kill that cannot land, or a corpse the daemon has not finished
// reaping, leaves the state as it was — still running, which no caller
// adopts.
func (r *Runner) killThenInspect(ctx context.Context, ref runs.AttemptRef,
	insp dockerapi.ContainerInspect,
) (dockerapi.ContainerInspect, bool) {
	name := agentName(ref)
	// Only a kill the daemon accepted is ours: not-found and conflict both
	// mean the container had already stopped on its own terms.
	err := r.engine.ContainerKill(ctx, name, "KILL")
	ourKill := err == nil
	if err != nil && !dockerapi.IsNotFound(err) && !dockerapi.IsConflict(err) {
		r.log.Warn("runner.reconcile.kill", "attempt", ref.String(), "error", err)
		return insp, false
	}
	// The daemon gets a bounded moment to reap it: an inspect taken too early
	// describes a container that has not exited yet.
	waitCtx, cancel := context.WithTimeout(ctx, agentCorpseGrace)
	defer cancel()
	if _, err := r.engine.ContainerWait(waitCtx, name); err != nil {
		r.log.Warn("runner.reconcile.wait", "attempt", ref.String(), "error", err)
	}
	after, err := r.engine.ContainerInspect(ctx, name)
	if err != nil {
		r.log.Warn("runner.reconcile.inspect", "attempt", ref.String(), "error", err)
		return insp, ourKill
	}
	return after, ourKill
}

// adopt collects a finished attempt exactly as Execute would have, then tears
// it down. Collection is the whole of adoption: if any part of it fails the
// attempt is reported lost, because a partial result would be persisted as if
// it were the phase's real output.
//
// It runs on the reconciliation the settlement produced, never on a fresh read
// of the disk: by now the conclusion may have deleted the manifest and bumped
// the store's version, and the in-memory copy is the only record of what the
// container was actually handed.
//
// The base SHA comes back off disk rather than out of a rebuilt RunSpec. The
// stage contract is written by the runner before staging and lives outside
// the workspace, so the agent could not have touched it, and reading it here
// keeps adoption from resolving a harness or opening a provider credential
// that no container is going to be handed.
func (r *Runner) adopt(ctx context.Context, ref runs.AttemptRef, insp dockerapi.ContainerInspect,
	rec credentialReconciliation,
) runs.InspectResult {
	defer r.teardown(ctx, ref)

	stage, err := readContract[gitops.StageInput](
		filepath.Join(r.attemptDir(ref), gitops.StageInputFile), stageReportLimit)
	if err != nil {
		r.log.Warn("runner.reconcile.stage_contract", "attempt", ref.String(), "error", err)
		return runs.InspectResult{}
	}
	credentialID, _ := uuid.Parse(rec.Manifest.CredentialID) // no manifest, no binding: uuid.Nil
	res, err := r.collectResult(ctx, collection{
		Ref:          ref,
		BaseSHA:      stage.BaseSHA,
		User:         r.runAs(),
		Kind:         rec.Manifest.HarnessKind,
		CredentialID: credentialID,
		ExitCode:     insp.State.ExitCode,
		OOM:          insp.State.OOMKilled,
	})
	// A typed failure is an ADOPTED result, not a lost attempt: the harness
	// finished and said why, and that answer — a cap window with its reset, a
	// dead credential — must reach the orchestrator intact. Only an untyped
	// error means adoption itself failed, which is what reconcile_lost is for.
	//
	// It carries the log pointer the worker path attaches to its own failures
	// (execute.go failureResult), so an operator reading a run that died in
	// reconciliation can still find the streams. The failure SNAPSHOT is
	// deliberately not here: it embeds a log tail that has to be redacted with
	// the attempt's secrets, and rebuilding that set from rec.Stored and the
	// conclusion's recovered tokens is Task 11's work — a snapshot redacted
	// with an empty set would look safe while carrying the credential verbatim.
	var oe runs.OutcomeError
	if errors.As(err, &oe) {
		r.log.Info("runner.reconcile.adopted_outcome", "attempt", ref.String(), "code", oe.Outcome.Code)
		return runs.InspectResult{
			Completed: true,
			Result:    runs.ExecutionResult{Artifacts: []runs.NewArtifact{r.logRefArtifact(ref)}},
			Outcome:   &oe.Outcome,
		}
	}
	if err != nil {
		r.log.Warn("runner.reconcile.collect", "attempt", ref.String(), "error", err)
		return runs.InspectResult{}
	}
	r.log.Info("runner.reconcile.adopted", "attempt", ref.String(), "artifacts", len(res.Artifacts))
	return runs.InspectResult{Completed: true, Result: res}
}
