package runs

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Recovery reads attempt refs back out of rows that only ever stored the
// string: the credential lease names its owner that way, and the janitor has
// nothing else to resolve a stale lease to a phase with. So the parse has to
// be the exact inverse of the format — including for the node ids that carry
// dashes, which is most of them.
func TestParseAttemptRef_RoundTrip(t *testing.T) {
	refs := []AttemptRef{
		{RunID: uuid.New(), NodeID: "review-and-fix", Attempt: 3},
		{RunID: uuid.New(), NodeID: "impl", Attempt: 1},
		{RunID: uuid.New(), NodeID: "a-b-c-d-e", Attempt: 12},
	}
	for _, want := range refs {
		got, err := ParseAttemptRef(want.String())
		if err != nil {
			t.Fatalf("ParseAttemptRef(%q): %v", want.String(), err)
		}
		if got != want {
			t.Errorf("ParseAttemptRef(%q) = %+v, want %+v", want.String(), got, want)
		}
	}
}

func TestParseAttemptRef_RefusesAnythingElse(t *testing.T) {
	valid := AttemptRef{RunID: uuid.New(), NodeID: "impl", Attempt: 2}.String()
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"garbage", "not-a-ref"},
		{"missing prefix", strings.TrimPrefix(valid, "caesar-")},
		{"foreign holder", "outside-holder"},
		{"unparseable run id", "caesar-not-a-uuid-here-at-all-nope-xxxxx-impl-1"},
		{"no attempt", "caesar-" + uuid.NewString() + "-impl"},
		{"non-numeric attempt", "caesar-" + uuid.NewString() + "-impl-last"},
		// Attempts are 1-based, so a zero ordinal is a ref nothing wrote.
		// A negative one is unrepresentable: the minus IS the separator, so
		// "impl--1" is the node id "impl-" at attempt 1, and parsing it that
		// way is the round trip, not a hole.
		{"zero attempt", "caesar-" + uuid.NewString() + "-impl-0"},
		{"empty node id", "caesar-" + uuid.NewString() + "--1"},
		{"attempt past int32", "caesar-" + uuid.NewString() + "-impl-9999999999"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseAttemptRef(tc.in); err == nil {
				t.Errorf("ParseAttemptRef(%q) = %+v, want an error", tc.in, got)
			}
		})
	}
}
