package runs

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// defaultDecisionPageLimit and maxDecisionPageLimit bound one page. They match
// the run list's numbers today and are stated separately anyway: a page size is
// POLICY, and two endpoints are free to disagree about it. Sharing the constant
// would make a change to one a silent change to the other.
const (
	defaultDecisionPageLimit int32 = 50
	maxDecisionPageLimit     int32 = 200
)

// DecidedGate is one decided gate as a HISTORY ROW, and it is the sibling of
// WaitingGate: the inbox row says what is still waiting, this one says what was
// settled. It is a different projection from the run page's GateDecision
// because it crosses runs, so it carries what identifies the run it came from.
//
// The type is NOT called Decision — that name is this package's gate VERDICT
// enum (approve | request_changes | reject), and a row is not a verdict.
//
// Attempt is the field this projection exists for. gate_decisions stores
// generation and keys the phase row it decided; the attempt lives only on that
// phase row, and a revision leaves two gate attempts under one node id. The
// history tab links with (RunID, NodeID, Generation, Attempt) — Task 9's
// selector triple — and a link missing the attempt half lands on whichever
// attempt is newest rather than the one decided.
type DecidedGate struct {
	ID                 uuid.UUID
	RunID              uuid.UUID
	TaskID             uuid.UUID
	TaskTitle          string
	NodeID             string
	Generation         int32
	Attempt            int32
	Decision           string
	Note               string
	ActedByEmail       string
	ActedAt            time.Time
	ExaminedArtifactID uuid.UUID
}

// DecisionFilter is one page request. All is the admin view; ProjectIDs is the
// exact grant set otherwise, and an empty one with All false is an empty page
// rather than everything.
type DecisionFilter struct {
	All        bool
	ProjectIDs []uuid.UUID
	Limit      int32
	Cursor     string
}

func clampDecisionPageLimit(n int32) int32 {
	switch {
	case n <= 0:
		return defaultDecisionPageLimit
	case n > maxDecisionPageLimit:
		return maxDecisionPageLimit
	default:
		return n
	}
}

// decisionPageParams turns a filter into sqlc params and the limit the page
// must be sliced to.
//
// It is a separate function from ListDecisions, and not merely inlined the way
// the run list does it, because every mistake available here is invisible in a
// passing integration test: a nil project slice that becomes SQL NULL, a cursor
// whose id half never reaches the row-value comparison, a page limit that
// disagrees with the slice. Each answers to a table test with no database in
// it.
func decisionPageParams(f DecisionFilter) (store.ListDecisionsPageParams, int32, error) {
	limit := clampDecisionPageLimit(f.Limit)
	params := store.ListDecisionsPageParams{
		AllProjects: f.All,
		// A nil slice encodes as SQL NULL, and `x = ANY(NULL)` is NULL rather
		// than false — the same empty page by accident. Normalizing keeps the
		// empty-grant-set answer a property of `= ANY('{}')`, which is what the
		// query's own comment claims it is.
		ProjectIds: append([]uuid.UUID{}, f.ProjectIDs...),
		// One extra row is how the next page is DETECTED. Counting is the
		// alternative and it is a second query over the same predicate.
		PageLimit: limit + 1,
	}
	if f.Cursor != "" {
		at, id, err := decodeKeysetCursor(f.Cursor)
		if err != nil {
			return store.ListDecisionsPageParams{}, 0, err
		}
		params.CursorActedAt = &at
		params.CursorID = uuid.NullUUID{UUID: id, Valid: true}
	}
	return params, limit, nil
}

// ListDecisions is one page of the cross-run decision history, newest first —
// the read behind the Approvals History tab, where the inbox shows what is
// still waiting.
//
// An empty cursor is the first page. A malformed one is a field error, never a
// silent first page: paging is the one place where being quietly wrong looks
// exactly like being right.
func (s *Service) ListDecisions(ctx context.Context, f DecisionFilter) ([]DecidedGate, string, error) {
	params, limit, err := decisionPageParams(f)
	if err != nil {
		return nil, "", err
	}
	rows, err := store.New(s.pool).ListDecisionsPage(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("runs: list decisions: %w", err)
	}
	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeKeysetCursor(last.ActedAt, last.ID)
	}
	out := make([]DecidedGate, 0, len(rows))
	for _, row := range rows {
		out = append(out, DecidedGate{
			ID:                 row.ID,
			RunID:              row.RunID,
			TaskID:             row.TaskID,
			TaskTitle:          row.TaskTitle,
			NodeID:             row.NodeID,
			Generation:         row.Generation,
			Attempt:            row.Attempt,
			Decision:           row.Decision,
			Note:               row.Note,
			ActedByEmail:       row.ActedByEmail,
			ActedAt:            row.ActedAt,
			ExaminedArtifactID: row.ExaminedArtifactID,
		})
	}
	return out, next, nil
}
