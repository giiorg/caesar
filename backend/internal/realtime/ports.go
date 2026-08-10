package realtime

import (
	"context"

	"github.com/google/uuid"
)

// EventSource is the read side of run_events. internal/app implements it
// over the runs store; realtime never imports runs.
type EventSource interface {
	// SnapshotWatermark returns the highest committed event id, read in
	// the SAME repeatable-read transaction the caller's snapshot was
	// computed in. Both values must come from one tx or an event
	// committing between them is lost — the gap this whole design exists
	// to prevent.
	SnapshotWatermark(ctx context.Context, f Filter) (Watermark, error)

	// Replay returns events with id > after that match f, oldest first,
	// at most limit rows. It is the catch-up path for both the initial
	// splice and Last-Event-ID resume.
	Replay(ctx context.Context, f Filter, after int64, limit int) ([]Event, error)

	// Since returns every event with id > after regardless of filter, for
	// the tailer's cursor advance. Filtering is per-subscriber.
	Since(ctx context.Context, after int64, limit int) ([]Event, error)

	// Oldest is the lowest retained event id, used to decide whether a
	// Last-Event-ID is too old to resume from and must re-snapshot.
	Oldest(ctx context.Context) (int64, error)
}

// Watermark is the high-water event id a snapshot was computed at.
type Watermark struct {
	EventID int64
}

// GrantResolver answers "what may this principal see, right now". It is
// re-consulted on an interval for the lifetime of every subscription so a
// revoked grant closes the stream (spec §4) without coupling realtime to
// identity.
type GrantResolver interface {
	ScopeFor(ctx context.Context, userID uuid.UUID) (Scope, error)
}
