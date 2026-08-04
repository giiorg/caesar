package runner

import (
	"context"
	"time"

	"github.com/giiorg/caesar/backend/internal/runs/runner/gitops"
)

// watchWorkspace samples the agent's workspace against its quota while the
// agent runs, and calls breach the first time it is over. The walk is capped
// at the quota itself, so a breached workspace is never traversed whole: the
// tree it is measuring is the agent's to grow.
//
// It fires at most once and then returns. The breach is a kill order, and an
// attempt is killed once — a repeating watchdog would only pile signals onto
// a container that is already going away.
func watchWorkspace(ctx context.Context, dir string, quotaBytes, quotaFiles int64,
	poll time.Duration, breach func(gitops.WalkStats),
) {
	quotaBytes, quotaFiles, poll = watchLimits(quotaBytes, quotaFiles, poll)
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			st, err := gitops.BoundedWalk(dir, true, quotaFiles, quotaBytes)
			if err != nil {
				// A workspace churning under the walk is normal, and one that
				// has been torn down is the end of the attempt, not a breach.
				continue
			}
			if st.Breached {
				breach(st)
				return
			}
		}
	}
}

// watchLimits normalizes what the watchdog was configured with. It exists for
// one reason: gitops.BoundedWalk treats a non-positive cap as no cap at all,
// so a zero reaching it would silently disarm the quota. Nothing here may
// mean "unlimited" — an unset limit is the pinned default.
func watchLimits(quotaBytes, quotaFiles int64, poll time.Duration) (int64, int64, time.Duration) {
	if quotaBytes <= 0 {
		quotaBytes = defaultWorkspaceQuotaBytes
	}
	if quotaFiles <= 0 {
		quotaFiles = defaultWorkspaceQuotaFiles
	}
	if poll <= 0 {
		poll = defaultQuotaPollInterval
	}
	return quotaBytes, quotaFiles, poll
}
