//go:build integration

package runs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// AppendEventsForTest exposes the tx-scoped appender to integration
// tests; production callers stay inside the service's transactions.
func AppendEventsForTest(ctx context.Context, tx pgx.Tx, now time.Time, evs []Event) error {
	return appendEvents(ctx, tx, now, evs)
}
