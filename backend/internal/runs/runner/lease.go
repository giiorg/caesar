package runner

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
)

const (
	// leaseHeartbeatInterval is how often a running attempt proves it is
	// still there. It is well inside the staleness window reconciliation
	// judges a lease by, so an attempt that is merely slow is never mistaken
	// for one whose worker died.
	leaseHeartbeatInterval = 30 * time.Second
)

// needsLease reports whether this binding must be serialized. Only kinds a
// running attempt can itself replace need it: the CLI refreshes an
// agent_auth_file in place, so two attempts sharing one would refresh each
// other out of validity (spec §2.1). Static kinds are shareable, and leasing
// them would only queue work behind itself.
//
// It is a method on the driver so the second condition can be ASKED rather
// than restated. Materializing an auth file is what puts the operator's real
// credential on the host, and the lease is the whole custody protocol around
// it — single-flight, heartbeat, sync-back, and the fence removeAuthDirFenced
// needs to clean the directory up. A pairing this driver materializes but no
// kind rule names would get the file and none of the protocol, silently, and
// the credential would then sit on disk until the orphan sweep at 25h. Keeping
// the two questions in one predicate is what makes that unreachable instead of
// merely absent.
func (d driver) needsLease(c runs.CredentialBinding) bool {
	if c.ID == uuid.Nil {
		return false
	}
	if c.Kind == kindAgentAuthFile {
		return true
	}
	cust, err := d.custodyOf(c.Kind)
	return err == nil && d.materializesAuthFile(cust)
}

func (r *Runner) heartbeatInterval() time.Duration {
	if r.opts.testLeaseHeartbeatInterval > 0 {
		return r.opts.testLeaseHeartbeatInterval
	}
	return leaseHeartbeatInterval
}

// acquireCredential takes the single-flight lease over the attempt's bound
// credential and, with it, the secret snapshot the grant covers.
//
// The busy branch keys off the ErrLeaseBusy sentinel, never off the error's
// class: the aggregate reports two different conflicts here — the lease is
// held, and this attempt already holds a lease on some other credential — and
// only the first is somebody else's turn. Treating the second as a wait would
// hide a design error behind an endless zero-charge retry loop.
func (r *Runner) acquireCredential(ctx context.Context, ref runs.AttemptRef, binding runs.CredentialBinding) (Lease, []byte, error) {
	// A runner wired without the credential aggregate cannot run a kind that
	// needs single-flight at all. Saying so is a deployment error, permanent
	// and loud — the alternative is a nil dereference somewhere further in.
	if r.leaser == nil {
		return Lease{}, nil, permanentf(runs.CodeInternalError,
			"credential kind %s needs a credential leaser, none is wired", binding.Kind)
	}
	lease, secret, err := r.leaser.Acquire(ctx, binding.ID, ref.String())
	switch {
	case errors.Is(err, ErrLeaseBusy):
		return Lease{}, nil, transientf(runs.CodeCredentialBusy,
			"credential %s lease held by another attempt", binding.ID)
	case err != nil:
		return Lease{}, nil, transientf(runs.CodeStagingFailed, "acquire credential lease: %v", err)
	}
	return lease, secret, nil
}

// startLeaseHeartbeat keeps the lease fresh for as long as the attempt runs
// and hands back the stop function that ends it. Stop joins the goroutine, so
// no heartbeat can outlive the attempt that owns it — a stray one would keep
// a dead attempt's lease alive against every claimant behind it.
//
// fenced is called when the store refuses the heartbeat: a takeover has
// advanced the epoch, custody belongs to the reconciler now, and this worker
// must stop rather than keep running against a credential it no longer owns.
// Anything else is the store being briefly unreachable, which the next tick
// retries: killing a healthy attempt over a blip costs more than a late
// heartbeat does.
func (r *Runner) startLeaseHeartbeat(ctx context.Context, credentialID uuid.UUID, ref runs.AttemptRef,
	epoch int64, fenced func(),
) func() {
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(r.heartbeatInterval())
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			err := r.leaser.Heartbeat(ctx, credentialID, ref.String(), epoch)
			switch {
			case err == nil:
			case xerrors.ClassOf(err) == xerrors.ClassConflict:
				r.log.Warn("runner.lease_fenced", "attempt", ref.String(),
					"credential", credentialID.String(), "epoch", epoch)
				fenced()
				return
			default:
				r.log.Warn("runner.lease_heartbeat", "attempt", ref.String(), "error", err)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}
}
