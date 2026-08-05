package runner

import (
	"context"
	"fmt"
)

// AdmitClaim is the free-space watermark (spec §8): typed backpressure
// BEFORE a claim exists, so refusal costs no attempt and no lease.
func (r *Runner) AdmitClaim(context.Context) error {
	free, err := r.freeBytes(r.opts.DataRoot)
	if err != nil {
		return fmt.Errorf("runner: statfs %s: %w", r.opts.DataRoot, err)
	}
	if free < r.opts.FreeBytesWatermark {
		return fmt.Errorf("runner: %d bytes free on %s, watermark %d", free, r.opts.DataRoot, r.opts.FreeBytesWatermark)
	}
	return nil
}
