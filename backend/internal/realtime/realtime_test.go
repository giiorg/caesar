package realtime

import (
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
		{name: "attention stream drops phase noise", kind: "phase.started", f: Filter{Attention: true, Scope: Scope{Projects: []uuid.UUID{projA}}}, want: false},
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
