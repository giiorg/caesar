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

// empty reports the scope that matches nothing and never will again — what a
// revoked or deactivated principal resolves to. It is the one thing that can
// end an IDLE stream, where there is no event to test allows against.
func (s Scope) empty() bool { return !s.All && len(s.Projects) == 0 }

// Filter selects the events one subscription receives. Exactly one of
// RunID (per-run stream) or Attention (global stream) is set.
//
// Build one with RunFilter or AttentionFilter rather than by hand. Two of the
// four combinations of those fields are wrong in ways nothing downstream can
// report — see Matches — and the constructors are the only shapes an HTTP
// surface should be able to produce.
type Filter struct {
	RunID     *uuid.UUID
	Attention bool
	Scope     Scope
}

// RunFilter is one run's stream within scope.
func RunFilter(runID uuid.UUID, s Scope) Filter {
	return Filter{RunID: &runID, Scope: s}
}

// AttentionFilter is the global attention stream within scope.
//
// It and RunFilter exist so that the ambiguous combinations are not something
// a handler reaches for. Both set is a per-run stream wearing the attention
// stream's clothes, and NEITHER set selects nothing at all — a stream that
// opens, writes not one event and heartbeats until the client gives up.
//
// What that buys is narrower than it looks, and the gap is the point. Filter's
// fields are exported, so `Filter{RunID: &id, Attention: true}` is legal Go and
// nothing here can stop it: the constructors make the two bad shapes
// unreachable THROUGH THEM, not unrepresentable. That holds only while every
// caller uses one. Both production callers do — realtime/api's
// handleStreamAttention and its ServeRunStream, which is the per-run adapter
// internal/app wires into runs/api — and review is the only thing enforcing
// it for the next one. A runtime check in Matches could close the gap for
// real, and was not added: it would have to choose a behaviour for a filter
// that is already a programming error, and every choice is silent.
func AttentionFilter(s Scope) Filter {
	return Filter{Attention: true, Scope: s}
}

// Matches decides delivery. Scope is checked FIRST and unconditionally:
// every path — snapshot, replay and live — funnels through here, so an
// event outside the caller's grants cannot reach a subscriber by any
// route.
//
// Selector precedence, for callers that build a Filter from request
// parameters: RunID WINS. A Filter with both RunID and Attention set is a
// per-run stream and the Attention flag is ignored; a Filter with neither
// selects nothing at all, whatever its Scope. Neither is worth relying on:
// RunFilter and AttentionFilter above are what a request-driven caller uses,
// and neither can express either combination.
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
