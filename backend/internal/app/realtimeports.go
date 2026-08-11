package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giiorg/caesar/backend/internal/identity"
	"github.com/giiorg/caesar/backend/internal/realtime"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// defaultSnapshotEvents is how many recent events a snapshot carries when
// nobody configured a limit. A zero would make every stream open on an empty
// backlog, which is a worse failure than a large one: the client would
// believe the run has no history.
const defaultSnapshotEvents = 500

// realtimeEventSource implements realtime.EventSource over the runs store.
// It lives here because realtime is a bounded context and never imports
// runs; this is the composition root, the one place that sees both.
type realtimeEventSource struct {
	pool *pgxpool.Pool
	// snapshotEvents caps Snapshot's backlog. Task 12 wires it from
	// config.Realtime.ReplayBatch; nothing here reads config.
	snapshotEvents int32
}

// The port is not consumed inside app, so this is what keeps a signature
// drift from surviving until production wiring.
var _ realtime.EventSource = realtimeEventSource{}

func newRealtimeEventSource(pool *pgxpool.Pool, snapshotEvents int) realtimeEventSource {
	if snapshotEvents <= 0 {
		snapshotEvents = defaultSnapshotEvents
	}
	return realtimeEventSource{pool: pool, snapshotEvents: rowLimit(snapshotEvents)}
}

// Snapshot returns the caller's backlog AND the watermark it was computed
// at, from ONE repeatable-read transaction. This is where gaplessness is
// won or lost, and two decisions carry it.
//
// FIRST, the read ORDER: watermark, then events. Both orders are safe under
// repeatable read, so the order is chosen for how each DEGRADES. Watermark
// first can only ever let the events read see something the watermark does
// not cover — a duplicate the subscriber dedups on id. Events first would
// let the watermark cover an event the snapshot never returned, which is a
// GAP, and a gap is the failure this whole design exists to prevent. The
// first statement is also what takes Postgres's transaction snapshot, so
// reading the watermark first is what pins the view the events read gets.
//
// SECOND, what is NOT here: there is deliberately no `id <= watermark`
// predicate on the events query. The transaction's snapshot already implies
// it, and writing it out would make the isolation level irrelevant — the
// result would look consistent under read committed too, and the invariant
// that every returned event is at or below the watermark would stop being
// falsifiable. That invariant is the acceptance evidence for this task.
//
// The transaction is read-only, so it is rolled back rather than committed:
// there is nothing to commit, and rollback releases the snapshot just as
// promptly.
func (s realtimeEventSource) Snapshot(ctx context.Context, f realtime.Filter) (realtime.Snapshot, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return realtime.Snapshot{}, fmt.Errorf("app: realtime snapshot: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := store.New(tx)
	head, err := q.MaxRunEventID(ctx)
	if err != nil {
		return realtime.Snapshot{}, fmt.Errorf("app: realtime snapshot: watermark: %w", err)
	}
	events, err := s.recent(ctx, q, f)
	if err != nil {
		return realtime.Snapshot{}, err
	}
	return realtime.Snapshot{Events: events, Watermark: realtime.Watermark{EventID: head}}, nil
}

// recent reads the most recent matching events, oldest first.
//
// The queries order DESCENDING because "the most recent N" is a LIMIT on a
// backwards index scan, and the slice is reversed here. That reversal is the
// one Go-side reordering in this file that is safe: it happens over the
// exact rows the LIMIT already chose, so it can neither hide a row nor
// invent one. Contrast the tailer's sort, which cannot repair a source that
// truncated the wrong order — which is why every ASCENDING query below
// orders in SQL.
func (s realtimeEventSource) recent(ctx context.Context, q *store.Queries, f realtime.Filter) ([]realtime.Event, error) {
	all, projects := scopeArgs(f.Scope)
	var (
		rows []store.RunEvent
		err  error
	)
	switch {
	case f.RunID != nil:
		rows, err = q.ListRecentRunEvents(ctx, store.ListRecentRunEventsParams{
			RunID: *f.RunID, AllProjects: all, ProjectIds: projects, Lim: s.snapshotEvents,
		})
	case f.Attention:
		rows, err = q.ListRecentAttentionEvents(ctx, store.ListRecentAttentionEventsParams{
			KindPatterns: realtime.AttentionKindPatterns(),
			AllProjects:  all, ProjectIds: projects, Lim: s.snapshotEvents,
		})
	default:
		// A filter with neither selector matches nothing at all, whatever its
		// scope (realtime.Filter.Matches). Answering with an empty backlog
		// rather than an error keeps that the HTTP edge's refusal to make.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("app: realtime snapshot: events: %w", err)
	}
	slices.Reverse(rows)
	return realtimeEvents(rows)
}

// Replay is the catch-up path for both the initial splice and a
// Last-Event-ID resume: matching events with id > after, oldest first.
//
// It runs OUTSIDE a transaction on purpose. A page is a cursor query, and
// the caller pages by the last id returned; a repeatable-read view would
// only pin it to a moment that is already past.
func (s realtimeEventSource) Replay(ctx context.Context, f realtime.Filter, after int64, limit int) ([]realtime.Event, error) {
	all, projects := scopeArgs(f.Scope)
	q := store.New(s.pool)
	var (
		rows []store.RunEvent
		err  error
	)
	switch {
	case f.RunID != nil:
		rows, err = q.ListRunEventsForRunAfter(ctx, store.ListRunEventsForRunAfterParams{
			RunID: *f.RunID, After: after, AllProjects: all, ProjectIds: projects, Lim: rowLimit(limit),
		})
	case f.Attention:
		rows, err = q.ListAttentionEventsAfter(ctx, store.ListAttentionEventsAfterParams{
			After:        after,
			KindPatterns: realtime.AttentionKindPatterns(),
			AllProjects:  all, ProjectIds: projects, Lim: rowLimit(limit),
		})
	default:
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("app: realtime replay after %d: %w", after, err)
	}
	return realtimeEvents(rows)
}

// Since is the tailer's drain: everything past the cursor, filter-blind,
// ORDER BY id LIMIT limit. The ordering is the contract — the tailer moves
// its cursor to the highest id it was handed, so a source that skipped lower
// ids and truncated at limit would strand them permanently, and no amount of
// sorting downstream recovers them.
func (s realtimeEventSource) Since(ctx context.Context, after int64, limit int) ([]realtime.Event, error) {
	rows, err := store.New(s.pool).ListRunEventsSince(ctx, store.ListRunEventsSinceParams{
		After: after, Lim: rowLimit(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("app: realtime since %d: %w", after, err)
	}
	return realtimeEvents(rows)
}

// Oldest is the lowest retained id: below it, a Last-Event-ID can no longer
// be resumed from and the subscriber must re-snapshot.
func (s realtimeEventSource) Oldest(ctx context.Context) (int64, error) {
	id, err := store.New(s.pool).MinRunEventID(ctx)
	if err != nil {
		return 0, fmt.Errorf("app: realtime oldest event: %w", err)
	}
	return id, nil
}

// Head is the current maximum id. It seeds the tailer's startup cursor, and
// a failure here is fatal to startup by design: there is no safe id to
// guess, and guessing 0 would replay the whole table into every stream.
func (s realtimeEventSource) Head(ctx context.Context) (int64, error) {
	id, err := store.New(s.pool).MaxRunEventID(ctx)
	if err != nil {
		return 0, fmt.Errorf("app: realtime head event: %w", err)
	}
	return id, nil
}

// scopeArgs is realtime.Scope in the two columns the queries take. All is a
// separate boolean because "every project" is not a project list: an admin's
// footprint is not enumerable, and an empty list is the default-deny case
// rather than a wildcard.
func scopeArgs(s realtime.Scope) (bool, []uuid.UUID) {
	if s.All {
		return true, nil
	}
	// pgx encodes a nil slice as NULL, and `project_id = ANY(NULL)` is NULL
	// rather than false — which the WHERE clause would drop anyway, but an
	// empty array says default-deny in the plan as well as in the result.
	if s.Projects == nil {
		return false, []uuid.UUID{}
	}
	return false, s.Projects
}

// rowLimit narrows a caller's row count onto the int32 the generated
// queries take. Both ends are clamped rather than converted: a limit past
// int32 would wrap into a NEGATIVE LIMIT, which Postgres rejects outright,
// turning a caller's arithmetic slip into a failed stream rather than a
// large page.
func rowLimit(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	}
	return int32(n)
}

func realtimeEvents(rows []store.RunEvent) ([]realtime.Event, error) {
	out := make([]realtime.Event, 0, len(rows))
	for _, r := range rows {
		e, err := realtimeEvent(r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// realtimeEvent decodes one row. The payload is decoded FRESH per row and
// never cached: the hub fans one Event out to every matching subscriber
// without copying the map, so two subscribers holding the same map would be
// one write away from corrupting each other's view.
func realtimeEvent(r store.RunEvent) (realtime.Event, error) {
	var payload map[string]any
	if len(r.Payload) > 0 {
		if err := json.Unmarshal(r.Payload, &payload); err != nil {
			return realtime.Event{}, fmt.Errorf("app: realtime: decode payload of event %d: %w", r.ID, err)
		}
	}
	return realtime.Event{
		ID:            r.ID,
		RunID:         r.RunID,
		ProjectID:     r.ProjectID,
		Kind:          r.Kind,
		Payload:       payload,
		OccurredAt:    r.OccurredAt,
		CorrelationID: r.CorrelationID,
	}, nil
}

// realtimeGrants implements realtime.GrantResolver over identity. It is
// re-consulted on an interval for the lifetime of every subscription, so a
// revoked grant closes the stream without coupling realtime to identity.
type realtimeGrants struct{ svc *identity.Service }

var _ realtime.GrantResolver = realtimeGrants{}

// ScopeFor answers what a principal may see RIGHT NOW, on the same branch
// the approvals inbox takes: an admin sees every project, a member sees
// exactly their grants. Sharing the branch is the point — "what a stream
// shows" and "what the approvals list shows" cannot be allowed to diverge.
//
// Two things differ, and both follow from this being a repeated question
// about a live stream rather than a one-shot answer to a request:
//
//   - the role is read from the database, not from the session that opened
//     the stream, so a demotion reaches a subscription that is already open;
//   - a deactivated principal gets the empty scope, which matches nothing.
//     Grant rows survive deactivation, so grants alone would keep a disabled
//     admin streaming forever. Nothing diverges from the inbox here: a
//     deactivated user cannot authenticate a request at all.
func (g realtimeGrants) ScopeFor(ctx context.Context, userID uuid.UUID) (realtime.Scope, error) {
	u, err := g.svc.GetUser(ctx, userID)
	if err != nil {
		return realtime.Scope{}, err
	}
	if !u.Active {
		return realtime.Scope{}, nil
	}
	if u.Role == identity.RoleAdmin {
		return realtime.Scope{All: true}, nil
	}
	grants, err := g.svc.GrantsForUser(ctx, userID)
	if err != nil {
		return realtime.Scope{}, err
	}
	projects := make([]uuid.UUID, 0, len(grants))
	for _, gr := range grants {
		projects = append(projects, gr.ProjectID)
	}
	return realtime.Scope{Projects: projects}, nil
}
