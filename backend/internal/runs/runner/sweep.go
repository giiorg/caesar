package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/giiorg/caesar/backend/internal/platform/dockerapi"
)

const (
	// sweepInterval is how often the maintenance loop looks for orphans.
	// Nothing it removes is urgent — everything is already a day past any
	// live run — so the loop is cheap rather than prompt.
	sweepInterval = 10 * time.Minute

	// orphanMaxAge is the wall-clock ceiling one run may occupy (24h) plus
	// slack. A container, network or attempt directory older than this is
	// unreachable by any live run: reconciliation owns everything younger,
	// and a resource this age means the process that owned it is gone.
	orphanMaxAge = 25 * time.Hour

	// sweepTimeout bounds one pass. A daemon that will not answer is next
	// tick's problem, not a stuck goroutine.
	sweepTimeout = 5 * time.Minute
)

// Run is the runner's maintenance loop: an orphan sweep every sweepInterval
// until the context is done. It is started by the composition root beside the
// orchestrator's own loop, and returns nil on shutdown — a maintenance pass
// that failed is logged, never fatal.
func (r *Runner) Run(ctx context.Context) error {
	tick := time.NewTicker(sweepInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		if err := r.SweepOrphans(ctx, orphanMaxAge); err != nil {
			r.log.Error("runner.sweep", "error", err)
		}
	}
}

// SweepOrphans removes what no live run can reach any more: containers and
// networks this runner labeled, and attempt directories, last touched more
// than olderThan ago. It is scoped by label and by age in both directions —
// an unlabeled container belongs to somebody else, and a young one belongs to
// a run that reconciliation is still entitled to adopt.
//
// The shared egress network is exempt: it is host-lifetime infrastructure
// created once and reused by every attempt, so its age says nothing about
// whether anything still needs it.
func (r *Runner) SweepOrphans(ctx context.Context, olderThan time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	cutoff := time.Now().Add(-olderThan)

	containers, listErr := r.engine.ContainerListByLabel(ctx, managedLabel)
	if listErr != nil {
		listErr = fmt.Errorf("list managed containers: %w", listErr)
	}
	networks, netErr := r.engine.NetworkListByLabel(ctx, managedLabel)
	if netErr != nil {
		netErr = fmt.Errorf("list managed networks: %w", netErr)
	}

	var staleContainers, staleNetworks []string
	for _, c := range containers {
		if time.Unix(c.Created, 0).Before(cutoff) {
			staleContainers = append(staleContainers, c.ID)
		}
	}
	for _, n := range networks {
		if n.Name == r.opts.EgressNetwork {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, n.Created)
		if err != nil {
			// A network whose age cannot be read is left alone: removing it
			// would be guessing at what it belongs to.
			r.log.Warn("runner.sweep.network_created", "network", n.Name, "created", n.Created, "error", err)
			continue
		}
		if created.Before(cutoff) {
			staleNetworks = append(staleNetworks, n.ID)
		}
	}

	// Containers first: a network still holding an endpoint cannot be
	// removed, and the endpoints worth removing are the ones just swept.
	r.removeByName(ctx, staleContainers, staleNetworks)
	dirs, dirErr := r.sweepAttemptDirs(cutoff)
	creds, credErr := r.sweepCredentialLeftovers(ctx, cutoff)
	if len(staleContainers) > 0 || len(staleNetworks) > 0 || dirs > 0 || creds > 0 {
		r.log.Info("runner.sweep", "containers", len(staleContainers), "networks", len(staleNetworks),
			"attempt_dirs", dirs, "credential_leftovers", creds, "older_than", olderThan.String())
	}
	return errors.Join(listErr, netErr, dirErr, credErr)
}

// sweepAttemptDirs removes the attempt directories left behind by processes
// that died before their own teardown, and reports how many went. The logs
// directory is deliberately not swept: logs are evidence, and their retention
// is a separate decision (Plan 13).
func (r *Runner) sweepAttemptDirs(cutoff time.Time) (int, error) {
	root := filepath.Join(r.opts.DataRoot, "attempts")
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", root, err)
	}
	removed := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(root, e.Name())
		if err := os.RemoveAll(path); err != nil {
			r.log.Warn("runner.sweep.attempt_dir", "path", path, "error", err)
			continue
		}
		removed++
	}
	return removed, nil
}

// sweepCredentialLeftovers reaps the credential material a conclusion
// deliberately left behind: an unscannable or quarantined attempt keeps its
// auth directory and manifest as evidence (spec §2.7), and nothing else ever
// comes back for them.
//
// The lease is the gate, not the age. A directory whose lease row still exists
// belongs to reconciliation however old it is — releasing is the ONLY thing
// that ends custody, and deleting the evidence under a reconciler that is about
// to read it would destroy the credential the attempt refreshed. Everything
// past orphanMaxAge with no lease behind it is unreachable by any live run.
func (r *Runner) sweepCredentialLeftovers(ctx context.Context, cutoff time.Time) (int, error) {
	if r.leaser == nil {
		// Without the aggregate there is no way to prove a leftover is not
		// lease-held, and an unproven removal is what the gate exists to stop.
		return 0, nil
	}
	removed := 0
	var errs []error
	for _, sweep := range []struct {
		dir string
		// refOf maps a directory entry back to the attempt ref that owns it:
		// the auth directory is named for the ref, the manifest is that name
		// plus .json.
		refOf func(name string) string
	}{
		{dir: filepath.Join(r.opts.DataRoot, "auth"), refOf: func(name string) string { return name }},
		{dir: r.manifestsDir(), refOf: func(name string) string { return strings.TrimSuffix(name, ".json") }},
	} {
		entries, err := os.ReadDir(sweep.dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("read %s: %w", sweep.dir, err))
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
			ref := sweep.refOf(e.Name())
			_, _, held, err := r.leaser.LeaseByAttempt(ctx, ref)
			if err != nil {
				errs = append(errs, fmt.Errorf("credential lease for %s: %w", ref, err))
				continue
			}
			if held {
				continue // reconciliation's, however old
			}
			path := filepath.Join(sweep.dir, e.Name())
			if err := os.RemoveAll(path); err != nil {
				r.log.Warn("runner.sweep.credential_leftover", "path", path, "error", err)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

// removeByName force-removes a set of containers and then a set of networks,
// tolerating anything that is already gone. The daemon accepts either a name
// or an id, which is what lets teardown pass the names it constructed from a
// ref and the sweep pass the ids it discovered.
//
// Everything here is best-effort by construction: a removal that fails is
// logged and the rest still run, because the caller is either a deferred
// cleanup with nowhere to report or a sweep whose next pass will try again.
func (r *Runner) removeByName(ctx context.Context, containers, networks []string) {
	for _, name := range containers {
		if err := r.engine.ContainerRemove(ctx, name, true); err != nil && !dockerapi.IsNotFound(err) {
			r.log.Warn("runner.remove.container", "container", name, "error", err)
		}
	}
	for _, name := range networks {
		if err := r.engine.NetworkRemove(ctx, name); err != nil && !dockerapi.IsNotFound(err) {
			r.log.Warn("runner.remove.network", "network", name, "error", err)
		}
	}
}
