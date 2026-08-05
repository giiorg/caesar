package runner

import (
	"context"
	"os"
	"path/filepath"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
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
func (r *Runner) Inspect(ctx context.Context, ref runs.AttemptRef) (runs.InspectResult, error) {
	insp, err := r.engine.ContainerInspect(ctx, agentName(ref))
	if dockerapi.IsNotFound(err) {
		// Nothing to adopt and nothing to remove: either the attempt never
		// got as far as an agent, or a teardown already ran.
		return runs.InspectResult{}, nil
	}
	if err != nil {
		// The daemon could not be asked, so nothing is known — and tearing
		// down what might still be a live attempt on the strength of a failed
		// inspect is the one move that could destroy work.
		return runs.InspectResult{}, transientf(runs.CodeInternalError,
			"inspect %s: %v", agentName(ref), err)
	}

	if insp.State.Status == "running" {
		// A container still running has no lease behind it any more. It is
		// killed rather than waited on: whatever it is doing, no worker is
		// listening for the answer.
		insp = r.killThenInspect(ctx, ref, insp)
	}

	// The attempt directory is what collection reads. Execute removes it as
	// the last thing it does, so a finished container beside a missing
	// directory is a worker that died after its own teardown — the work is
	// gone, and only the corpse is left to clear.
	if insp.State.Status != "exited" || insp.State.ExitCode != 0 || !r.attemptDirExists(ref) {
		r.teardown(ctx, ref)
		return runs.InspectResult{}, nil
	}
	return r.adopt(ctx, ref, insp), nil
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
// reports what it left behind. A kill that cannot land, or a corpse the
// daemon has not finished reaping, leaves the state as it was — still
// running, which no caller adopts.
func (r *Runner) killThenInspect(ctx context.Context, ref runs.AttemptRef,
	insp dockerapi.ContainerInspect,
) dockerapi.ContainerInspect {
	name := agentName(ref)
	err := r.engine.ContainerKill(ctx, name, "KILL")
	if err != nil && !dockerapi.IsNotFound(err) && !dockerapi.IsConflict(err) { // conflict = already stopped
		r.log.Warn("runner.reconcile.kill", "attempt", ref.String(), "error", err)
		return insp
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
		return insp
	}
	return after
}

// adopt collects a finished attempt exactly as Execute would have, then tears
// it down. Collection is the whole of adoption: if any part of it fails the
// attempt is reported lost, because a partial result would be persisted as if
// it were the phase's real output.
//
// The base SHA comes back off disk rather than out of a rebuilt RunSpec. The
// stage contract is written by the runner before staging and lives outside
// the workspace, so the agent could not have touched it, and reading it here
// keeps adoption from resolving a harness or opening a provider credential
// that no container is going to be handed.
func (r *Runner) adopt(ctx context.Context, ref runs.AttemptRef, insp dockerapi.ContainerInspect) runs.InspectResult {
	defer r.teardown(ctx, ref)

	stage, err := readContract[gitops.StageInput](
		filepath.Join(r.attemptDir(ref), gitops.StageInputFile), stageReportLimit)
	if err != nil {
		r.log.Warn("runner.reconcile.stage_contract", "attempt", ref.String(), "error", err)
		return runs.InspectResult{}
	}
	res, err := r.collectResult(ctx, collection{
		Ref:      ref,
		BaseSHA:  stage.BaseSHA,
		User:     r.runAs(),
		ExitCode: insp.State.ExitCode,
		OOM:      insp.State.OOMKilled,
	})
	if err != nil {
		r.log.Warn("runner.reconcile.collect", "attempt", ref.String(), "error", err)
		return runs.InspectResult{}
	}
	r.log.Info("runner.reconcile.adopted", "attempt", ref.String(), "artifacts", len(res.Artifacts))
	return runs.InspectResult{Completed: true, Result: res}
}
