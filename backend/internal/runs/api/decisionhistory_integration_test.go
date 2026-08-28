//go:build integration

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"

	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
)

// decidedHistory is what stageDecisionHistory wrote. The tests read specific
// rows out of it rather than re-deriving them from the response, because "the
// decision the approver made on project A" is a property of the fixture and
// re-computing it from the answer under test is how an assertion agrees with a
// bug.
type decidedHistory struct {
	runA uuid.UUID
	runB uuid.UUID
}

// stageDecisionHistory puts FOUR decisions on the board, two per project, and
// every one of the four is load-bearing.
//
// Per project, `gatedRun` leaves a run with a spent revision round behind it —
// a real request_changes row at (generation 1, attempt 1) — and its live gate
// waiting at (generation 2, attempt 3). Deciding that live gate through the
// real endpoint adds the second row.
//
//	TWO PROJECTS, so "a member sees only theirs" is not equally explained by
//	  "there is only one project's worth of history to see".
//	TWO ROUNDS ON ONE NODE, so the pair of rows sharing a node id is the
//	  ordinary case rather than a contrived one — and their (generation,
//	  attempt) pairs differ in BOTH halves.
//	AN ATTEMPT THAT IS NOT THE GENERATION, courtesy of stageGate's deliberate
//	  gap: the live gate is attempt 3 of generation 2. A projection that read
//	  gd.generation into both columns, or dropped the phase_runs join and
//	  defaulted the attempt, passes every fixture where the two agree — and in
//	  every run this system can actually produce, they do agree.
func (h *harness) stageDecisionHistory(t *testing.T) decidedHistory {
	t.Helper()
	// Project A's task is the RUNNER's and the APPROVER decides it, so nothing
	// here depends on the shipped policy permitting self-approval.
	_, runA, _ := h.gatedRun(t, "runner-tok", h.repoID, "ship it")
	// Project B has exactly one granted user, so its run is necessarily its
	// approver's own. Distinct actors across the two projects is what makes the
	// scoping subtest able to name whose decision leaked.
	_, runB, _ := h.gatedRun(t, "approver-b-tok", h.repoB, "ship it too")

	h.approveGate(t, runA, "approver-tok")
	h.approveGate(t, runB, "approver-b-tok")

	h.assertDecisionFixtureIsNotTheSimpleCase(t)
	return decidedHistory{runA: runA, runB: runB}
}

func (h *harness) approveGate(t *testing.T, runID uuid.UUID, token string) {
	t.Helper()
	resp := h.do(t, http.MethodPost, decisionPath(runID, "gate"), token,
		runsapi.GateDecisionRequest{Decision: "approve", Note: "ship it"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("approve as %s: status %d, want 200", token, resp.StatusCode)
	}
}

// assertDecisionFixtureIsNotTheSimpleCase proves against the DATABASE that the
// fixture has the two properties the subtests below rely on. A fixture that
// quietly degraded — a stageGate that stopped writing its prior round, an
// approve that landed on a different node — turns the attempt and scoping
// subtests green for the wrong reason.
func (h *harness) assertDecisionFixtureIsNotTheSimpleCase(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	var total int
	if err := h.pool.QueryRow(ctx, `SELECT count(*) FROM gate_decisions`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("fixture staged %d decisions, want 4 (two rounds in each of two projects)", total)
	}

	// The whole reason this endpoint joins phase_runs. If every decided gate's
	// attempt equalled its generation, the join would be unfalsifiable here and
	// the subtest asserting it would pass against a projection that never made
	// it.
	var diverged int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM gate_decisions gd
		 JOIN phase_runs pr ON pr.id = gd.phase_run_id
		 WHERE pr.attempt <> gd.generation`).Scan(&diverged); err != nil {
		t.Fatal(err)
	}
	if diverged == 0 {
		t.Fatal("no decided gate has an attempt differing from its generation — " +
			"the phase_runs join is untestable against this fixture")
	}
}

func (h *harness) listDecisions(t *testing.T, token, query string) runsapi.DecisionPageResponse {
	t.Helper()
	resp := h.do(t, http.MethodGet, "/api/v1/approvals/decisions"+query, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/approvals/decisions%s = %d, want 200", query, resp.StatusCode)
	}
	return decodeInto[runsapi.DecisionPageResponse](t, resp)
}

func TestListDecisions(t *testing.T) {
	h := newHarness(t)
	staged := h.stageDecisionHistory(t)

	t.Run("the history arm and the waiting arm both answer", func(t *testing.T) {
		// The route-order question, asserted rather than assumed: Go's mux
		// prefers the more specific literal pattern, so registering
		// /approvals/decisions must not shadow /approvals — in either
		// direction. A shadowed waiting arm would answer this page's envelope,
		// which is why each assertion reads the OTHER endpoint's key.
		waiting := h.do(t, http.MethodGet, "/api/v1/approvals", "admin-tok", nil)
		if waiting.StatusCode != http.StatusOK {
			t.Fatalf("GET /approvals = %d, want 200", waiting.StatusCode)
		}
		if inbox := decodeInto[runsapi.ApprovalListResponse](t, waiting); inbox.Approvals == nil {
			t.Error("GET /approvals answered without an approvals array — the history arm is shadowing it")
		}
		if page := h.listDecisions(t, "admin-tok", ""); page.Decisions == nil {
			t.Error("GET /approvals/decisions answered without a decisions array")
		}
	})

	t.Run("an admin sees every project's decisions, newest first", func(t *testing.T) {
		page := h.listDecisions(t, "admin-tok", "")
		if len(page.Decisions) != 4 {
			t.Fatalf("admin saw %d decisions, want 4", len(page.Decisions))
		}
		assertNewestFirst(t, page.Decisions)
		// The two approves are the recent pair; the two request_changes rows
		// were stamped 45 minutes back. A page ordered ASC puts them the other
		// way round, and so does one ordered by id alone.
		for i, d := range page.Decisions[:2] {
			if d.Decision != "approve" {
				t.Errorf("row %d is %q, want the two newest rows to be the approvals", i, d.Decision)
			}
		}
	})

	t.Run("a member sees only the projects they hold a grant on", func(t *testing.T) {
		page := h.listDecisions(t, "approver-tok", "")
		if len(page.Decisions) != 2 {
			t.Fatalf("project-A approver saw %d decisions, want 2", len(page.Decisions))
		}
		for _, d := range page.Decisions {
			if d.RunID != staged.runA.String() {
				t.Errorf("a decision of run %s reached a member granted only project A", d.RunID)
			}
			if d.TaskTitle != "ship it" {
				t.Errorf("task_title = %q, want the project-A task", d.TaskTitle)
			}
		}

		other := h.listDecisions(t, "approver-b-tok", "")
		if len(other.Decisions) != 2 {
			t.Fatalf("project-B approver saw %d decisions, want 2", len(other.Decisions))
		}
		for _, d := range other.Decisions {
			if d.RunID != staged.runB.String() {
				t.Errorf("a decision of run %s reached a member granted only project B", d.RunID)
			}
		}
	})

	t.Run("a revision leaves two rows on one node, at the attempts that were decided", func(t *testing.T) {
		// THE ATTEMPT IS THE POINT OF THIS ENDPOINT. gate_decisions stores the
		// generation and keys the phase row; the attempt exists only on that
		// phase row, and Task 18 links with all four of
		// (run_id, node_id, generation, attempt) because a revision leaves two
		// decided gate attempts under one node id.
		page := h.listDecisions(t, "approver-tok", "")
		byDecision := map[string]runsapi.DecisionResponse{}
		for _, d := range page.Decisions {
			if d.NodeID != "gate" {
				t.Errorf("node_id = %q, want the gate node", d.NodeID)
			}
			byDecision[d.Decision] = d
		}
		if len(byDecision) != 2 {
			t.Fatalf("decisions by verdict = %v, want a request_changes and an approve", byDecision)
		}
		prior, live := byDecision["request_changes"], byDecision["approve"]
		if prior.Generation != 1 || prior.Attempt != 1 {
			t.Errorf("the superseded round is (generation %d, attempt %d), want (1, 1)", prior.Generation, prior.Attempt)
		}
		// (2, 3) and not (2, 2): stageGate forces the divergence precisely so a
		// projection reading the generation into both columns cannot pass.
		if live.Generation != 2 || live.Attempt != liveGateAttempt {
			t.Errorf("the decided round is (generation %d, attempt %d), want (2, %d)",
				live.Generation, live.Attempt, liveGateAttempt)
		}
		if live.ID == prior.ID || live.ExaminedArtifactID == prior.ExaminedArtifactID {
			t.Error("the two rounds share an id or an examined artifact — they are one row rendered twice")
		}
	})

	t.Run("a row carries the actor's address and the note, not a uuid", func(t *testing.T) {
		page := h.listDecisions(t, "approver-tok", "")
		var approve runsapi.DecisionResponse
		for _, d := range page.Decisions {
			if d.Decision == "approve" {
				approve = d
			}
		}
		if approve.ActedByEmail != "approver@example.com" {
			t.Errorf("acted_by_email = %q, want the deciding approver's address", approve.ActedByEmail)
		}
		if approve.Note != "ship it" {
			t.Errorf("note = %q, want the note the decision carried", approve.Note)
		}
		if approve.ActedAt.IsZero() {
			t.Error("acted_at is zero")
		}
		if approve.TaskID == "" || approve.TaskID == uuid.Nil.String() {
			t.Errorf("task_id = %q, want the run's task", approve.TaskID)
		}
	})

	t.Run("an ungranted member gets an empty page, not a refusal", func(t *testing.T) {
		// Seeing nothing and being refused are different answers, and this
		// endpoint only ever gives the first. The empty ProjectIDs slice is
		// what makes it so: `= ANY('{}')` is the empty set, where a nil one
		// would be `= ANY(NULL)` and the same empty page by accident.
		page := h.listDecisions(t, "outsider-tok", "")
		if len(page.Decisions) != 0 {
			t.Errorf("an ungranted member saw %d decisions, want none", len(page.Decisions))
		}
		if page.NextCursor != nil {
			t.Errorf("next_cursor = %q on an empty page, want null", *page.NextCursor)
		}
	})

	t.Run("no cookie is a 401", func(t *testing.T) {
		if resp := h.do(t, http.MethodGet, "/api/v1/approvals/decisions", "", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("a bad cursor and a bad limit are 400 with a field error, not 500", func(t *testing.T) {
		for _, tc := range []struct{ query, field string }{
			{"?cursor=!!!", "cursor"},
			{"?limit=soon", "limit"},
			{"?limit=0", "limit"},
			{"?limit=-3", "limit"},
		} {
			resp := h.do(t, http.MethodGet, "/api/v1/approvals/decisions"+tc.query, "admin-tok", nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("GET /approvals/decisions%s = %d, want 400", tc.query, resp.StatusCode)
				continue
			}
			// A 400 with no field error is a validation failure the form layer
			// cannot route, which is the whole point of the envelope.
			pd := decodeInto[httpx.ProblemDetails](t, resp)
			blamed := false
			for _, fe := range pd.Errors {
				if fe.Field == tc.field {
					blamed = true
				}
			}
			if !blamed {
				t.Errorf("GET /approvals/decisions%s errors = %+v, want one blaming %q", tc.query, pd.Errors, tc.field)
			}
		}
	})

	t.Run("a two-row walk covers the page exactly once", func(t *testing.T) {
		// A WIDER PAGE THAN THE STRADDLE TEST'S, and that is the whole reason
		// this subtest exists. With limit=1 a page's first row IS its last, so
		// a next_cursor minted from the wrong end of the page is invisible —
		// measured: mutating the cursor to rows[0] left the entire suite green
		// until this walk was added. At limit=2 that mutant re-emits the second
		// row of every page.
		var order []string
		seen := map[string]int{}
		cursor := ""
		for page := 0; ; page++ {
			if page > 4 {
				t.Fatal("the walk did not terminate over four rows — the cursor is not advancing")
			}
			query := "?limit=2"
			if cursor != "" {
				query += "&cursor=" + cursor
			}
			got := h.listDecisions(t, "admin-tok", query)
			assertNewestFirst(t, got.Decisions)
			for _, d := range got.Decisions {
				seen[d.ID]++
				order = append(order, d.ID)
			}
			if got.NextCursor == nil {
				break
			}
			cursor = *got.NextCursor
		}
		if len(seen) != 4 {
			t.Fatalf("the walk returned %d distinct decisions, want 4: %v", len(seen), seen)
		}
		for id, n := range seen {
			if n != 1 {
				t.Errorf("decision %s arrived %d times across the walk, want exactly once", id, n)
			}
		}
		// The walked order must be the unpaginated order. A cursor that pages
		// correctly but from a different sort key returns every row once and
		// still hands the client a shuffled history.
		whole := h.listDecisions(t, "admin-tok", "")
		for i, d := range whole.Decisions {
			if order[i] != d.ID {
				t.Errorf("walked row %d is %s, want %s — paging reordered the history", i, order[i], d.ID)
			}
		}
	})

	t.Run("an oversized limit is clamped rather than refused", func(t *testing.T) {
		page := h.listDecisions(t, "admin-tok", "?limit=100000")
		if len(page.Decisions) != 4 {
			t.Errorf("saw %d decisions, want the whole fixture", len(page.Decisions))
		}
	})
}

// TestListDecisions_EmptyPage is its own test because it needs a board with
// nothing on it, and this endpoint's own fixture puts four rows there.
//
// It is REACHABLE here, unlike the audit read's equivalent: a decision requires
// somebody to have decided a gate, and nothing about minting a session writes
// one. That asymmetry is worth stating, because the audit endpoint's empty-page
// test had to truncate its table to get here.
func TestListDecisions_EmptyPage(t *testing.T) {
	h := newHarness(t)
	page := h.listDecisions(t, "admin-tok", "")
	if page.Decisions == nil {
		t.Fatal("decisions is null on an empty page — the envelope must carry an empty array")
	}
	if len(page.Decisions) != 0 {
		t.Errorf("saw %d decisions on an untouched board, want none", len(page.Decisions))
	}
	if page.NextCursor != nil {
		t.Errorf("next_cursor = %q, want null", *page.NextCursor)
	}
}

// TestListDecisions_EqualActedAtStraddlesAPageBoundary is the keyset's real
// test, and it needs a fixture no ordinary path produces.
//
// acted_at is stamped by the DATABASE clock, which is transaction_timestamp()
// and frozen for the transaction — so two decisions made in one transaction
// share it exactly. Nothing in the shipped decide path puts two gates in one
// transaction, so the rows are inserted directly: THE STORE IS THE SUBJECT
// HERE, not DecideGate, and what is being asserted is that the cursor's id half
// carries a page boundary that its timestamp half cannot.
//
// With every acted_at distinct, the id half of the cursor is dead weight and a
// keyset that dropped it pages correctly anyway. With two rows sharing one, a
// cursor carrying only the timestamp either re-emits the row it just returned
// or skips it — forever, and only for the pages that happen to end there.
func TestListDecisions_EqualActedAtStraddlesAPageBoundary(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	task := h.createTaskInRepo(t, "runner-tok", h.repoID, "twins")
	runID, err := uuid.Parse(task.LatestRunID)
	if err != nil {
		t.Fatal(err)
	}
	baseSHA, _ := h.parkAtGate(t, runID, 2)

	// Two decided gate rounds on one run: each needs its own phase row (the FK
	// is UNIQUE) and its own examined artifact.
	type round struct {
		phaseID    uuid.UUID
		artifactID uuid.UUID
		generation int32
		attempt    int32
	}
	rounds := []round{
		{generation: 1, attempt: 1},
		{generation: 2, attempt: 2},
	}
	for i := range rounds {
		r := &rounds[i]
		r.artifactID = h.stageArtifact(t, runID, "impl", "patch", r.attempt, r.generation, 60-int32(i)*10,
			patchPayload(stagedTreeHash, baseSHA))
		r.phaseID = h.stagePhase(t, runID, "gate", "succeeded", r.attempt, r.generation, 50-int32(i)*10, mins(45))
	}

	// ONE TRANSACTION for both stamps, and that is the whole mechanism. Two
	// separate statements would each read a slightly different instant and the
	// rows meant to SHARE an acted_at would land microseconds apart — which is
	// exactly the fixture that cannot fail.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, r := range rounds {
		if _, err := tx.Exec(ctx,
			`INSERT INTO gate_decisions (phase_run_id, run_id, node_id, generation, decision,
			                             examined_artifact_id, note, acted_by, acted_at)
			 VALUES ($1, $2, 'gate', $3, 'approve', $4, '', $5, now())`,
			r.phaseID, runID, r.generation, r.artifactID, h.approver.UserID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The fixture proves its own premise against the database. Written without
	// the transaction first, this reads 2 and the walk below cannot fail.
	var distinct int
	if err := h.pool.QueryRow(ctx, `SELECT count(DISTINCT acted_at) FROM gate_decisions`).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 1 {
		t.Fatalf("the two decisions have %d distinct acted_at values, want 1 — "+
			"the cursor's id half is untested when they differ", distinct)
	}

	// A limit of one puts the boundary BETWEEN the twins, which is the only
	// placement that exercises the id half at all.
	seen := map[string]int{}
	cursor := ""
	for page := 0; page < 4; page++ {
		query := "?limit=1"
		if cursor != "" {
			query += "&cursor=" + cursor
		}
		got := h.listDecisions(t, "admin-tok", query)
		for _, d := range got.Decisions {
			seen[d.ID]++
		}
		if got.NextCursor == nil {
			// TWO pages, not three. The next page is detected by asking for one
			// row more than the limit, so the page carrying the last row
			// already knows it is the last — a walk that ends on an extra empty
			// page would mean the detection row was being counted as data.
			if page != 1 {
				t.Errorf("the walk ended after %d pages, want 2 (one row each, the second knowing it is last)", page+1)
			}
			break
		}
		if page == 3 {
			t.Fatal("the walk did not terminate over two rows — the cursor is not advancing")
		}
		cursor = *got.NextCursor
	}
	if len(seen) != 2 {
		t.Fatalf("the walk returned %d distinct decisions, want 2: %v", len(seen), seen)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("decision %s arrived %d times across the walk, want exactly once", id, n)
		}
	}
}

// assertNewestFirst holds the ordering as a PROPERTY of the whole page rather
// than as a claim about two named rows: an ORDER BY flipped to ASC, or one that
// lost its id tiebreak, has to survive every adjacent pair here.
func assertNewestFirst(t *testing.T, rows []runsapi.DecisionResponse) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		if cur.ActedAt.After(prev.ActedAt) {
			t.Errorf("row %d (%s) is newer than row %d (%s) — the page is not newest-first",
				i, cur.ActedAt.Format(time.RFC3339Nano), i-1, prev.ActedAt.Format(time.RFC3339Nano))
		}
		if cur.ActedAt.Equal(prev.ActedAt) && cur.ID > prev.ID {
			t.Errorf("rows %d and %d share acted_at and are ordered by ascending id — "+
				"the tiebreak disagrees with the cursor's", i-1, i)
		}
	}
}
