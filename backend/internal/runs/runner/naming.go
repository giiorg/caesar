package runner

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// Every resource an attempt creates is named from its execution identity
// (spec §2) and labeled with it, so reconciliation and the orphan sweep can
// find the whole set from the ref alone — no side table, no bookkeeping that
// can drift from the daemon's truth.
func agentName(ref runs.AttemptRef) string    { return ref.String() }
func gatewayName(ref runs.AttemptRef) string  { return ref.String() + "-egress" }
func stageName(ref runs.AttemptRef) string    { return ref.String() + "-stage" }
func extractName(ref runs.AttemptRef) string  { return ref.String() + "-extract" }
func validateName(ref runs.AttemptRef) string { return ref.String() + "-validate" }
func networkName(ref runs.AttemptRef) string  { return ref.String() + "-net" }

func labels(ref runs.AttemptRef) map[string]string {
	return map[string]string{
		"caesar.managed": "true",
		"caesar.run":     ref.RunID.String(),
		"caesar.node":    ref.NodeID,
		"caesar.attempt": strconv.FormatInt(int64(ref.Attempt), 10),
	}
}

// attemptDir holds everything one attempt writes; teardown removes it whole.
func (r *Runner) attemptDir(ref runs.AttemptRef) string {
	return filepath.Join(r.opts.DataRoot, "attempts", ref.String())
}

// logsDir outlives the attempt: logs are evidence, retention is Plan 13's.
func (r *Runner) logsDir(ref runs.AttemptRef) string {
	return filepath.Join(r.opts.DataRoot, "logs", ref.String())
}

// relPath renders a path the way artifacts and events carry it: relative to
// the data root, so a stored reference survives the root being remounted
// somewhere else. A path outside the root is carried whole — inventing a
// relative spelling for it would point at the wrong file.
func (r *Runner) relPath(p string) string {
	rel, err := filepath.Rel(r.opts.DataRoot, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return rel
}

// hostPath translates a path this process sees into the path the Docker
// daemon sees. Bind sources are resolved by the daemon, so a containerized
// control plane must hand over the host's spelling of its data root. Paths
// outside the data root are returned unchanged — there is nothing to
// translate, and silently rewriting them would invent mounts.
func (r *Runner) hostPath(p string) string {
	if r.opts.DataHostRoot == "" || r.opts.DataHostRoot == r.opts.DataRoot {
		return p
	}
	if p == r.opts.DataRoot {
		return r.opts.DataHostRoot
	}
	if rest, ok := strings.CutPrefix(p, r.opts.DataRoot+string(filepath.Separator)); ok {
		return filepath.Join(r.opts.DataHostRoot, rest)
	}
	return p
}
