package realtime

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFilter_Matches(t *testing.T) {
	t.Parallel()
	projA, projB := uuid.New(), uuid.New()
	runA, otherRun := uuid.New(), uuid.New()
	ev := Event{ID: 7, RunID: runA, ProjectID: projA, Kind: "gate.waiting"}

	for _, tc := range []struct {
		name string
		// kind overrides ev.Kind; empty keeps the attention-worthy default.
		// Each row states the event it tests, so two rows with the same
		// filter and opposite expectations stay readable as such.
		kind string
		f    Filter
		want bool
	}{
		{name: "run stream matches its run", f: Filter{RunID: &runA, Scope: Scope{Projects: []uuid.UUID{projA}}}, want: true},
		{name: "run stream rejects another run", f: Filter{RunID: &otherRun, Scope: Scope{Projects: []uuid.UUID{projA}}}, want: false},
		{name: "attention stream takes attention kinds", f: Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{projA}}}, want: true},
		// Plan 12a put `phase.` on this stream: live phase dots and the run
		// timeline are driven by transitions, so what was noise here is now
		// the payload. The row below it is what keeps the kind test
		// falsifiable — with all four namespaces carried, every kind
		// runs/events.go defines matches, and a Matches that ignored the kind
		// entirely would pass every other row in this table.
		{name: "attention stream takes phase transitions", kind: "phase.started", f: Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{projA}}}, want: true},
		{name: "attention stream drops a kind in no namespace", kind: "audit.recorded", f: Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{projA}}}, want: false},
		// The attention stream carries every project's gate/publish/run
		// events, so it is the path where a missing scope check leaks across
		// tenants rather than merely across runs.
		{name: "attention stream still obeys scope", f: Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{projB}}}, want: false},
		{name: "scope excludes other projects", f: Filter{RunID: &runA, Scope: Scope{Projects: []uuid.UUID{projB}}}, want: false},
		{name: "admin scope takes everything", f: Filter{RunID: &runA, Scope: Scope{All: true}}, want: true},
		// The zero Scope is the one a bug hands you — an unresolved grant, a
		// struct built field by field. It must deny.
		{name: "zero scope denies", f: Filter{RunID: &runA}, want: false},
		// A filter that selects neither a run nor the attention stream
		// selects nothing. Without this the admin-scoped no-selector case is
		// a firehose rather than an empty stream.
		{name: "no selector denies", f: Filter{Scope: Scope{All: true}}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := ev
			if tc.kind != "" {
				in.Kind = tc.kind
			}
			if got := tc.f.Matches(in); got != tc.want {
				t.Errorf("Matches(%+v) = %v, want %v", in, got, tc.want)
			}
		})
	}
}

// TestAttentionFilterCarriesPhaseTransitions is the transport half of live
// phase dots: a client watching Home or a run's timeline learns that a node
// moved only if the transition reaches the global stream at all.
//
// The kinds are spelled out rather than derived from runs/events.go because
// realtime is a bounded context and imports no other one — which is exactly
// why this assertion belongs here: a phase namespace renamed on the producing
// side would otherwise go unnoticed until a dot stopped moving in a browser.
func TestAttentionFilterCarriesPhaseTransitions(t *testing.T) {
	t.Parallel()
	f := AttentionFilter(Scope{All: true})
	for _, kind := range []string{
		"phase.materialized", "phase.started", "phase.succeeded",
		"phase.retried", "phase.dead", "phase.reconciling",
		"phase.log_truncated", "phase.egress_denied",
	} {
		if !f.Matches(Event{Kind: kind}) {
			t.Errorf("the attention stream drops %s; no phase transition would reach a client", kind)
		}
	}
}

// TestAttentionKindPatterns_CarryNoLikeMetacharacters is what makes the
// prefix-to-LIKE translation safe rather than merely true today.
//
// '%' and '_' are LIKE metacharacters, and '_' matches ANY single character.
// A namespace like "gate_review." would therefore make the EventSource's SQL
// over-select relative to Matches — the snapshot and the replay would carry
// events the live path drops — silently, and only against a database. Adding
// such a prefix has to fail here, in a unit test, and force a deliberate
// escape rather than be found as a stream that disagrees with itself.
func TestAttentionKindPatterns_CarryNoLikeMetacharacters(t *testing.T) {
	t.Parallel()
	patterns := AttentionKindPatterns()
	if len(patterns) != len(attentionPrefixes) {
		t.Fatalf("got %d patterns for %d prefixes", len(patterns), len(attentionPrefixes))
	}
	for i, prefix := range attentionPrefixes {
		if strings.ContainsAny(prefix, "%_") {
			t.Errorf("prefix %q contains a LIKE metacharacter: the SQL would over-select "+
				"relative to Matches, so escape it before appending the wildcard", prefix)
		}
		if want := prefix + "%"; patterns[i] != want {
			t.Errorf("pattern %d = %q, want %q", i, patterns[i], want)
		}
	}
}
