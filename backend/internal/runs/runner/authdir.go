package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
)

// materializeAuthDir writes the attempt's credential where its CLI looks for
// it: an attempt-private host directory, bound into the container by
// startAgent. A directory rather than a single-file bind because the CLI owns
// its own write strategy — truncate in place, sibling temp file, lock file —
// and a file bind would survive only the first of those (spec §2.4).
//
// The bytes are written exactly as the store granted them: the runner is a
// courier here, and re-serializing an auth.json it only partly understands
// would hand the CLI a file the operator never supplied.
func (r *Runner) materializeAuthDir(ref runs.AttemptRef, d driver, secret []byte) error {
	dir := r.authDir(ref)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create auth dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, d.authMount.file), secret, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", d.authMount.file, err)
	}
	return nil
}

// removeAuthDirFenced removes the attempt's auth directory and manifest, but
// only after proving this worker still owns the lease at its epoch — a
// heartbeat immediately before removal (spec §2.4). The database fence alone
// cannot stop a paused worker's deferred cleanup: by the time it resumes, a
// reconciler may own the directory. Fenced → relinquish, reconciliation
// cleans up under its own epoch.
func (r *Runner) removeAuthDirFenced(ctx context.Context, ref runs.AttemptRef, credentialID uuid.UUID, epoch int64) bool {
	// A runner wired without the credential aggregate cannot prove it still
	// owns the lease, and an unproven removal is exactly what the fence
	// exists to stop — refuse, never panic.
	if r.leaser == nil {
		r.log.Warn("runner.auth_cleanup_fenced", "attempt", ref.String(), "error", "no credential leaser wired")
		return false
	}
	if err := r.leaser.Heartbeat(ctx, credentialID, ref.String(), epoch); err != nil {
		r.log.Warn("runner.auth_cleanup_fenced", "attempt", ref.String())
		return false
	}
	if err := os.RemoveAll(r.authDir(ref)); err != nil {
		r.log.Warn("runner.auth_dir_remove", "attempt", ref.String(), "error", err)
	}
	if err := os.Remove(r.manifestPath(ref)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		r.log.Warn("runner.manifest_remove", "attempt", ref.String(), "error", err)
	}
	return true
}
