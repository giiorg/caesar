// Package realtime owns the SSE streams: a single per-process tailer over
// run_events, in-memory fan-out to bounded per-subscriber queues, and the
// snapshot-then-deltas splice that makes a stream gapless (spec §4).
//
// It is a bounded context and therefore imports no other context. What it
// needs from runs and identity arrives through the ports in ports.go,
// wired in internal/app.
package realtime

import (
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// attentionPrefixes are the event-kind namespaces the global stream
// carries. Plan 08 deliberately gave gates and publications their own
// namespaces (see runs/events.go) so this filter is a prefix test and
// never an enumeration that rots as kinds are added.
var attentionPrefixes = []string{"gate.", "publish.", "run."}

// AttentionKindPatterns is attentionPrefixes as SQL LIKE patterns, for the
// EventSource implementation to push the kind test down into the query.
//
// It exists so the prefix list has ONE definition. The snapshot and replay
// queries must select exactly what Matches selects: filtering in Go after a
// LIMIT would truncate a page that happened to be all non-attention rows,
// and a second hand-written prefix list in SQL would rot the moment a
// namespace is added here. None of the prefixes contains a LIKE
// metacharacter, so appending '%' is the whole translation.
func AttentionKindPatterns() []string {
	out := make([]string, 0, len(attentionPrefixes))
	for _, p := range attentionPrefixes {
		out = append(out, p+"%")
	}
	return out
}

// Event is one streamable observation. It mirrors a run_events row; the
// id is commit-ordered (allocated under the run_event_counter lock), which
// is what makes it usable as both a cursor and a dedup key.
//
// An Event is IMMUTABLE once constructed. The hub fans one out to every
// matching subscriber, so the struct is copied but Payload's backing map is
// not: a subscriber that writes to Payload is racing every other subscriber
// and corrupting their view of the same event. Read it; never write it.
type Event struct {
	ID        int64
	RunID     uuid.UUID
	ProjectID uuid.UUID
	Kind      string
	// Payload is shared across all subscribers of this event — read-only.
	Payload       map[string]any
	OccurredAt    time.Time
	CorrelationID string
}

// Scope is a principal's current authorization footprint. All is the
// admin case; Projects is the exact grant set otherwise. An empty Scope
// matches nothing — default-deny.
type Scope struct {
	All      bool
	Projects []uuid.UUID
}

func (s Scope) allows(project uuid.UUID) bool {
	return s.All || slices.Contains(s.Projects, project)
}

// Filter selects the events one subscription receives. Exactly one of
// RunID (per-run stream) or Attention (global stream) is set.
type Filter struct {
	RunID     *uuid.UUID
	Attention bool
	Scope     Scope
}

// Matches decides delivery. Scope is checked FIRST and unconditionally:
// every path — snapshot, replay and live — funnels through here, so an
// event outside the caller's grants cannot reach a subscriber by any
// route.
//
// Selector precedence, for callers that build a Filter from request
// parameters: RunID WINS. A Filter with both RunID and Attention set is a
// per-run stream and the Attention flag is ignored; a Filter with neither
// selects nothing at all, whatever its Scope. Reject the ambiguous
// combination at the HTTP edge rather than relying on this order.
func (f Filter) Matches(e Event) bool {
	if !f.Scope.allows(e.ProjectID) {
		return false
	}
	if f.RunID != nil {
		return *f.RunID == e.RunID
	}
	if !f.Attention {
		return false
	}
	for _, p := range attentionPrefixes {
		if strings.HasPrefix(e.Kind, p) {
			return true
		}
	}
	return false
}
