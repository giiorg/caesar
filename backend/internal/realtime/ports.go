package realtime

import (
	"context"

	"github.com/google/uuid"
)

// EventSource is the read side of run_events. internal/app implements it
// over the runs store; realtime never imports runs.
type EventSource interface {
	// Snapshot returns the caller's current state AND the high-watermark it
	// was computed at, both from ONE repeatable-read transaction. Returning
	// them together is what makes the gap unrepresentable.
	Snapshot(ctx context.Context, f Filter) (Snapshot, error)

	// Replay returns events with id > after that match f, oldest first,
	// at most limit rows. It is the catch-up path for both the initial
	// splice and Last-Event-ID resume.
	//
	// limit MUST be positive. A non-positive limit yields an empty page,
	// which a pager cannot tell apart from "no more events" — it would read
	// as a caught-up stream while events are still waiting. That is a
	// programming error, not a mode: it is not defaulted to a batch size,
	// because inventing a page nobody asked for hides the bug instead of
	// stopping it. Callers take the limit from configuration, which is
	// validated positive.
	Replay(ctx context.Context, f Filter, after int64, limit int) ([]Event, error)

	// Since returns every event with id > after regardless of filter, for
	// the tailer's cursor advance. Filtering is per-subscriber.
	//
	// It MUST return rows oldest first, at most limit of them — ORDER BY id
	// LIMIT limit, never a truncation of some other order. That ordering is
	// what makes the cursor advance gapless: the tailer moves its cursor to
	// the highest id it was handed, so a source that skipped lower ids and
	// then truncated at limit would strand them permanently. Sorting the
	// batch afterwards cannot recover them; only the query's order can.
	Since(ctx context.Context, after int64, limit int) ([]Event, error)

	// Oldest is the lowest retained event id, used to decide whether a
	// Last-Event-ID is too old to resume from and must re-snapshot.
	Oldest(ctx context.Context) (int64, error)

	// Head is the current maximum event id. It seeds the tailer's startup
	// cursor, so that a process starting up follows what happens next
	// rather than replaying the whole table into the hub.
	Head(ctx context.Context) (int64, error)
}

// Watermark is the high-water event id a snapshot was computed at.
type Watermark struct {
	EventID int64
}

// Snapshot is a caller's current state together with the high-watermark
// it was computed at. Returning them from one call is what makes the gap
// unrepresentable: they come from ONE repeatable-read transaction.
type Snapshot struct {
	Events    []Event
	Watermark Watermark
}

// GrantResolver answers "what may this principal see, right now". It is
// re-consulted on an interval for the lifetime of every subscription so a
// revoked grant closes the stream (spec §4) without coupling realtime to
// identity.
type GrantResolver interface {
	ScopeFor(ctx context.Context, userID uuid.UUID) (Scope, error)
}
