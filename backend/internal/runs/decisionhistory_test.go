package runs

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// The page-request half of GET /approvals/decisions is a pure function from a
// filter to sqlc params, which is what makes these questions answerable without
// a database: every one of them is a way for the endpoint to page WRONGLY —
// quietly, and only under conditions no ordinary fixture reproduces.

func TestDecisionPageParamsClampsTheLimit(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int32
		want int32
	}{
		// The +1 is not decoration: it is how the next page is DETECTED, so
		// every expectation here is the clamped limit plus that extra row.
		{"unset takes the default", 0, defaultDecisionPageLimit + 1},
		{"negative takes the default", -7, defaultDecisionPageLimit + 1},
		{"one is honoured", 1, 2},
		{"in range is honoured", 25, 26},
		{"the cap is honoured", maxDecisionPageLimit, maxDecisionPageLimit + 1},
		// CLAMPED, NOT REFUSED: the cap is this server's protection, not a
		// contract the caller agreed to, and a page of 200 is a complete answer
		// to "give me 1000".
		{"oversize is clamped", 100_000, maxDecisionPageLimit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params, limit, err := decisionPageParams(DecisionFilter{Limit: tc.in})
			if err != nil {
				t.Fatalf("params: %v", err)
			}
			if params.PageLimit != tc.want {
				t.Errorf("PageLimit = %d, want %d", params.PageLimit, tc.want)
			}
			// The caller slices the page with `limit`, so a limit that
			// disagreed with PageLimit-1 would return the detection row to the
			// client as data.
			if limit != tc.want-1 {
				t.Errorf("limit = %d, want %d — the page must be sliced to the limit the params asked for", limit, tc.want-1)
			}
		})
	}
}

func TestDecisionPageParamsNormalizesAnEmptyGrantSet(t *testing.T) {
	// `x = ANY(NULL)` is NULL, not false — so a nil slice reaching the driver
	// produces an empty page for a reason nobody wrote down, and the day the
	// predicate changes shape it produces the wrong page instead. The empty
	// answer must be a property of `= ANY('{}')`, which is what the query's own
	// comment claims it is.
	params, _, err := decisionPageParams(DecisionFilter{ProjectIDs: nil})
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.ProjectIds == nil {
		t.Error("ProjectIds is nil — it encodes as SQL NULL, and `= ANY(NULL)` is NULL rather than an empty set")
	}
	if len(params.ProjectIds) != 0 {
		t.Errorf("ProjectIds = %v, want empty", params.ProjectIds)
	}
	if params.AllProjects {
		t.Error("AllProjects = true for a filter that did not ask for it — an ungranted member would see every project")
	}
}

func TestDecisionPageParamsCarriesTheScope(t *testing.T) {
	ids := []uuid.UUID{
		uuid.MustParse("018f5f0e-0000-7000-8000-00000000000a"),
		uuid.MustParse("018f5f0e-0000-7000-8000-00000000000b"),
	}
	params, _, err := decisionPageParams(DecisionFilter{All: true, ProjectIDs: ids})
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if !params.AllProjects {
		t.Error("AllProjects = false, want true")
	}
	if len(params.ProjectIds) != len(ids) || params.ProjectIds[0] != ids[0] || params.ProjectIds[1] != ids[1] {
		t.Errorf("ProjectIds = %v, want %v", params.ProjectIds, ids)
	}
}

func TestDecisionPageParamsSplitsTheCursorIntoBothHalves(t *testing.T) {
	at := time.Date(2026, 8, 16, 12, 0, 0, 123456789, time.UTC)
	id := uuid.MustParse("018f5f0e-0000-7000-8000-000000000001")

	params, _, err := decisionPageParams(DecisionFilter{Cursor: encodeKeysetCursor(at, id)})
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.CursorActedAt == nil {
		t.Fatal("CursorActedAt is nil — the query reads it as 'no cursor' and answers with page one")
	}
	// NANOSECONDS SURVIVE OR THE PAGE BOUNDARY MOVES. acted_at comes off the
	// database clock, frozen per transaction, so decisions sharing a stamp to
	// the microsecond are ordinary rather than rare.
	if !params.CursorActedAt.Equal(at) {
		t.Errorf("CursorActedAt = %v, want %v", *params.CursorActedAt, at)
	}
	// BOTH HALVES OR THE KEYSET IS A GUESS. The predicate is a row-value
	// comparison; an id half left invalid makes it `(acted_at, id) < (stamp,
	// NULL)`, which is NULL for every row — an empty page from a perfectly
	// good cursor.
	if !params.CursorID.Valid {
		t.Fatal("CursorID is invalid — the row-value comparison then has a NULL half and matches nothing")
	}
	if params.CursorID.UUID != id {
		t.Errorf("CursorID = %v, want %v", params.CursorID.UUID, id)
	}
}

func TestDecisionPageParamsLeavesTheFirstPageUnbounded(t *testing.T) {
	params, _, err := decisionPageParams(DecisionFilter{})
	if err != nil {
		t.Fatalf("params: %v", err)
	}
	if params.CursorActedAt != nil {
		t.Errorf("CursorActedAt = %v for an empty cursor, want nil", *params.CursorActedAt)
	}
	if params.CursorID.Valid {
		t.Error("CursorID is valid for an empty cursor — page one would be filtered by a zero uuid")
	}
}

func TestDecisionPageParamsRefusesACursorItDidNotIssue(t *testing.T) {
	// A malformed cursor is an ordinary thing to receive — a truncated URL, a
	// stale bookmark — and it must be a FIELD ERROR naming the parameter. A
	// silent page-one is the alternative, and paging is the one place where
	// being quietly wrong looks exactly like being right.
	_, _, err := decisionPageParams(DecisionFilter{Cursor: "!!!"})
	if err == nil {
		t.Fatal("a garbage cursor was accepted")
	}
	if got := xerrors.ClassOf(err); got != xerrors.ClassInvalid {
		t.Errorf("class = %v, want %v", got, xerrors.ClassInvalid)
	}
	var xe *xerrors.Error
	if !errors.As(err, &xe) {
		t.Fatalf("error is not an *xerrors.Error: %T", err)
	}
	if len(xe.Fields) != 1 || xe.Fields[0].Field != "cursor" {
		t.Errorf("fields = %+v, want exactly one naming cursor", xe.Fields)
	}
}
