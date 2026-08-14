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
// RunID (per-run stream) or Attention (global stream) SHOULD be set: the
// constructors below are what make that so, and Matches states what the other
// two combinations do rather than pretending they cannot be built.
//
// Build one with RunFilter or AttentionFilter rather than by hand. Two of the
// four combinations of those fields are wrong in ways nothing downstream can
// report — see Matches — and the constructors are the only shapes an HTTP
// surface should be able to produce.
type Filter struct {
	RunID     *uuid.UUID
	Attention bool
	Scope     Scope
	// runProject is the project the RunID's run belongs to. It is what lets a
	// grant re-check end a per-run stream over a QUIESCENT run: "the scope no
	// longer covers this run's project" is not otherwise computable, because
	// the only other way to learn a run's project is from one of its events and
	// a quiescent run supplies none.
	//
	// Three properties carry the safety here, and they are not the same one
	// said three ways — each closes a failure the others leave open.
	//
	// A POINTER is what makes OMISSION safe. Any Filter literal leaves it nil,
	// nil is read as "not supplied", and the stream falls back to the
	// event-driven close in follow. That holds whatever the field's visibility,
	// which is worth stating because the design note this came from argued it
	// the other way round: with a VALUE field an omission would be the zero
	// uuid, a scope that is neither All nor explicitly listing zero denies it,
	// and the first re-check would close every stream built that way — a silent
	// deny of a principal whose grants are intact. The pointer, not the export
	// rules, is what makes that unreachable.
	//
	// The pointer also keeps "not supplied" distinct from "the project whose id
	// happens to be all zeros". Nothing in the schema forbids the latter —
	// projects.id has a uuidv7 DEFAULT, not a non-zero CHECK — so an imported or
	// hand-inserted row can hold one, and a zero-uuid sentinel would have
	// exempted exactly that run's streams from the check while they went on
	// looking healthy.
	//
	// UNEXPORTED is what makes a WRONG value unreachable, which is the failure
	// the pointer does not cover. RunFilter is then the only door, and it takes
	// the project the HTTP surface has already resolved and authorized against;
	// a literal cannot substitute a project nobody checked.
	//
	// POSITIONAL on that constructor is the last part: on the supported path,
	// forgetting it is a compile error rather than a quiet nil.
	runProject *uuid.UUID
}

// RunProject is the project the run belongs to, and whether the Filter carries
// one at all — false on any Filter not built by RunFilter.
//
// The bool is not decoration: a caller cannot otherwise tell "no project" from
// "the zero project", which is the same conflation the pointer exists to avoid,
// merely moved to the accessor.
//
// Read-only ON PURPOSE, and the asymmetry with the unexported field is the
// point: what makes an exported field dangerous here is that a caller can SET
// it wrongly, which no accessor can do. Without this, whether the adapter
// actually threaded the project id is observable only inside this package —
// and the wiring that could drop it spans three (runs/api → realtime/api →
// here), so the assertion that matters would have nowhere to live.
func (f Filter) RunProject() (uuid.UUID, bool) {
	if f.runProject == nil {
		return uuid.Nil, false
	}
	return *f.runProject, true
}

// RunFilter is one run's stream within scope. projectID is the run's OWN
// project — see Filter.runProject for why it is a parameter and not a field a
// caller may fill in later.
func RunFilter(runID, projectID uuid.UUID, s Scope) Filter {
	return Filter{RunID: &runID, runProject: &projectID, Scope: s}
}

// AttentionFilter is the global attention stream within scope.
//
// It and RunFilter exist so that the ambiguous combinations are not something
// a handler reaches for. Both set is a per-run stream wearing the attention
// stream's clothes, and NEITHER set selects nothing at all — a stream that
// opens, writes not one event and heartbeats until the client gives up.
//
// What that buys is narrower than it looks, and the gap is the point. Filter's
// SELECTOR fields are exported, so `Filter{RunID: &id, Attention: true}` is
// legal Go and nothing here can stop it: the constructors make the two bad
// shapes unreachable THROUGH THEM, not unrepresentable. (runProject is the one
// field held to a stricter standard, and it says there why the same gap was
// not acceptable for it.) That holds only while every
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
