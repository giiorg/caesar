//go:build integration

package api_test

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/httpx"

	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
)

// stagedRuns is what stageRunsForListing wrote, in the order it created the
// tasks. The test reads specific rows out of it rather than re-deriving them,
// because "the newest run of project A" is a property of the fixture's
// timestamps and re-computing it in the assertion would let the two drift.
type stagedRuns struct {
	// newestInA is the project-A run the "a card needs no second request"
	// subtest reads. It is the one with PARTIAL phases — see stageRunsForListing.
	newestInA uuid.UUID
	// noPhases is the project-A run with NO phase rows at all. A run whose
	// attempt is 0 rather than 1 only exists while nothing has been claimed, and
	// it is the only row that can tell "the newest phase attempt" apart from a
	// projection that defaults the ordinal.
	noPhases uuid.UUID
	// taskWithRuns is a task id the task filter can be pointed at.
	taskWithRuns uuid.UUID
	// onWorkflowB is the one run whose task points at the SECOND workflow. It
	// is on project A's repo, not project B's, so the repo and workflow
	// partitions of the fixture cross-cut: neither filter can be mistaken for
	// the other, and composing them is not the same answer as either alone.
	onWorkflowB uuid.UUID
	// onSecondVersion is a run whose task pins version 2 of the FIRST workflow.
	// A workflow_id filter that resolved the workflow to a single version id —
	// its latest, say — drops this row while answering every other assertion
	// correctly.
	onSecondVersion uuid.UUID
}

// listPageWalkSize is the page size the paging subtest walks with, and it is a
// constant because the fixture is TUNED to it: the two runs sharing a
// created_at have to fall either side of one of this walk's page boundaries, or
// the cursor's id half is never the thing that decides a page break. Change one
// and assertSharedPairStraddlesAPageBoundary tells you to change the other.
const listPageWalkSize = 2

// stageRunsForListing builds the fixture the whole file runs against: five runs
// in project A, two in project B, mixed statuses, one of them waiting.
//
// THREE PROPERTIES OF IT ARE LOAD-BEARING, and each is asserted here rather
// than assumed, because every one of them is a way for a subtest below to pass
// while testing nothing:
//
//  1. TWO RUNS SHARE A created_at, AND A PAGE BOUNDARY FALLS BETWEEN THEM. The
//     keyset predicate is a row-value comparison over (created_at, id); with
//     every timestamp distinct, the id half is dead weight and a cursor that
//     dropped it pages correctly anyway — but so does one where the shared pair
//     sits wholly inside a single page, because then no cursor is ever minted
//     from a timestamp another row also carries. Ranks are asserted, not
//     described: this comment claimed the straddle for a round before anything
//     checked it.
//  2. The timestamps are otherwise DISTINCT and spread. The harness clock is a
//     fake pinned to one instant, so every run intake writes gets the SAME
//     created_at — which would make the ordering entirely id-driven and leave
//     the created_at half of the sort key untested. They are rewritten off the
//     database clock, the way every other fixture in this suite does it.
//  3. The run the card subtest reads has SOME materialized phases and not all.
//     phase_summary emits one entry per node of the compiled definition
//     regardless, so a run with zero phases and a run with every phase both
//     satisfy "one entry per node" — and only a partially materialized one can
//     tell that projection apart from one that lists materialized rows.
//  4. THE REPO AND WORKFLOW PARTITIONS CROSS-CUT, and one task pins a
//     non-latest version. Both are what make the two filters this fixture feeds
//     falsifiable: with every workflow-B run also being a repo-B run, one
//     filter implemented as the other passes; with every task on version 1, a
//     workflow filter that resolved a workflow to one version id passes too.
func (h *harness) stageRunsForListing(t *testing.T) stagedRuns {
	t.Helper()
	ctx := context.Background()

	// agoMin per task, in creation order. The two 50s are the shared pair; the
	// project-A list's newest is the unique 20, so the card subtest does not
	// depend on how a tie breaks.
	//
	// The workflow and version columns exist for property 4. Exactly one row
	// points at workflow B, and it is on project A's REPO — putting it on repo B
	// would let `repo_id=B` and `workflow_id=B` return the same rows, and either
	// filter could then be implemented as the other. Exactly one row pins
	// version 2 of workflow A, and it is a repo-B row, so the second version is
	// not a project-A coincidence either.
	//
	// Newest-first these read 20, 30, 40, 50, 50, 60, 70 — so the pair occupies
	// ranks 4 and 5, and a two-row walk ends its second page between them. That
	// placement is what assertSharedPairStraddlesAPageBoundary holds; moving the
	// pair to 35/35 (ranks 3 and 4, both inside page two) keeps every other
	// property of this fixture true and makes the paging subtest stop testing
	// the id half of the cursor.
	staged := []struct {
		repo     uuid.UUID
		workflow uuid.UUID
		version  int32
		title    string
		agoMin   int32
		status   string
		started  bool
		done     bool
	}{
		{h.repoID, h.workflowID, gatedVersion, "oldest in A", 70, "waiting", true, false},
		{h.repoID, h.workflowID, gatedVersion, "second in A", 60, "succeeded", true, true},
		{h.repoID, h.workflowB, gatedVersion, "shared timestamp one", 50, "failed", true, true},
		{h.repoID, h.workflowID, gatedVersion, "shared timestamp two", 50, "pending", false, false},
		{h.repoID, h.workflowID, gatedVersion, "newest in A", 20, "running", true, false},
		{h.repoB, h.workflowID, planGateVersion, "older in B", 40, "running", true, false},
		{h.repoB, h.workflowID, gatedVersion, "newer in B", 30, "pending", false, false},
	}

	var out stagedRuns
	runIDs := make([]uuid.UUID, 0, len(staged))
	for i, s := range staged {
		task := h.createTaskOnWorkflowVersion(t, "admin-tok", s.repo, s.title, s.workflow, s.version)
		runID, err := uuid.Parse(task.LatestRunID)
		if err != nil {
			t.Fatal(err)
		}
		runIDs = append(runIDs, runID)
		if i == 0 {
			out.taskWithRuns, err = uuid.Parse(task.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		if i == 3 {
			out.noPhases = runID
		}
		if i == 4 {
			out.newestInA = runID
		}
		if i == 2 {
			out.onWorkflowB = runID
		}
		if i == 5 {
			out.onSecondVersion = runID
		}
	}

	// ONE TRANSACTION for all seven timestamps, and that is the whole reason
	// property 1 holds. Postgres now() is transaction_timestamp(), frozen for
	// the transaction — so seven separate statements would each read a slightly
	// different instant and the two rows meant to SHARE a created_at would land
	// microseconds apart. It was written that way first and the fixture check
	// below caught it: 0 shared values, and a paging subtest that could not
	// have failed for a cursor missing its id half.
	//
	// The database clock rather than a Go one, for the reason every other
	// fixture here uses it: the app clock this harness injects is a fake pinned
	// to a fixed date, and the query sorts by the database's.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for i, s := range staged {
		if _, err := tx.Exec(ctx,
			`UPDATE runs SET status = $2,
			        created_at = now() - make_interval(mins => $3::int),
			        started_at = CASE WHEN $4::boolean THEN now() - make_interval(mins => $3::int) END,
			        finished_at = CASE WHEN $5::boolean THEN now() - make_interval(mins => $3::int - 5) END
			 WHERE id = $1`,
			runIDs[i], s.status, s.agoMin, s.started, s.done); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The newest project-A run gets TWO of its four nodes materialized. The
	// pinned definition is gatedDoc — plan, impl, gate, publish — so the gate
	// and the publish have no row, which is what a dim dot means.
	h.stagePhase(t, out.newestInA, "plan", "succeeded", 1, 1, 18, mins(17))
	h.stagePhase(t, out.newestInA, "impl", "running", 2, 1, 16, nil)

	h.assertListingFixtureIsNotTheSimpleCase(t, out)
	return out
}

// assertListingFixtureIsNotTheSimpleCase proves the three properties above
// against the database rather than against the code that meant to write them.
// A fixture that quietly degraded — one UPDATE that matched nothing, a clock
// that collapsed every timestamp — turns the paging and phase-summary subtests
// green for the wrong reason, which is the failure mode this whole file is
// built to avoid.
func (h *harness) assertListingFixtureIsNotTheSimpleCase(t *testing.T, staged stagedRuns) {
	t.Helper()
	ctx := context.Background()

	var shared int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM (
		     SELECT created_at FROM runs GROUP BY created_at HAVING count(*) > 1
		 ) dup`).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	if shared != 1 {
		t.Fatalf("fixture has %d shared created_at values, want exactly 1 — "+
			"the cursor's id half is untested without one", shared)
	}
	h.assertSharedPairStraddlesAPageBoundary(t)

	var distinct, total int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(DISTINCT created_at), count(*) FROM runs`).Scan(&distinct, &total); err != nil {
		t.Fatal(err)
	}
	if total != 7 {
		t.Fatalf("fixture staged %d runs, want 7", total)
	}
	if distinct != total-1 {
		t.Fatalf("fixture has %d distinct created_at values over %d runs, want %d — "+
			"the created_at half of the sort key is untested when they collapse",
			distinct, total, total-1)
	}

	var materialized int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(DISTINCT node_id) FROM phase_runs WHERE run_id = $1`,
		staged.newestInA).Scan(&materialized); err != nil {
		t.Fatal(err)
	}
	nodes := h.nodeCountOfWorkflow(t)
	if materialized == 0 || materialized >= nodes {
		t.Fatalf("the card subtest's run has %d of %d nodes materialized, want some but not all — "+
			"a projection that listed only materialized rows would satisfy the assertion otherwise",
			materialized, nodes)
	}

	// And one run with NOTHING materialized, which is what makes "attempt is the
	// newest phase attempt" distinguishable from "attempt starts at 1".
	var untouched int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM phase_runs WHERE run_id = $1`,
		staged.noPhases).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if untouched != 0 {
		t.Fatalf("the run staged as unclaimed has %d phase rows, want 0 — "+
			"attempt 0 is only meaningful on a run nothing has claimed", untouched)
	}
	h.assertRepoAndWorkflowPartitionsCrossCut(t, staged)
}

// assertRepoAndWorkflowPartitionsCrossCut is property 4, read off the rows the
// filters actually query rather than off the table that meant to write them.
//
// It asserts the SHAPE the two filter subtests need and not the row ids they
// happen to use: that one repo holds runs of more than one workflow, that one
// workflow spans more than one repo, and that one task pins a version that is
// not the only one of its workflow. Any of the three collapsing turns a filter
// subtest green against an implementation that filtered by the OTHER column, or
// by version id instead of workflow id — and the collapse is invisible from the
// subtest, which would still watch a proper subset come back.
func (h *harness) assertRepoAndWorkflowPartitionsCrossCut(t *testing.T, staged stagedRuns) {
	t.Helper()
	ctx := context.Background()

	var workflowsOnRepoA, reposOnWorkflowA int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(DISTINCT wv.workflow_id)
		 FROM runs r JOIN tasks t ON t.id = r.task_id
		 JOIN workflow_versions wv ON wv.id = t.workflow_version_id
		 WHERE t.repo_id = $1`, h.repoID).Scan(&workflowsOnRepoA); err != nil {
		t.Fatal(err)
	}
	if workflowsOnRepoA < 2 {
		t.Fatalf("repo A's runs span %d workflows, want at least 2 — "+
			"a workflow_id filter implemented as a repo_id filter passes otherwise",
			workflowsOnRepoA)
	}
	if err := h.pool.QueryRow(ctx,
		`SELECT count(DISTINCT t.repo_id)
		 FROM runs r JOIN tasks t ON t.id = r.task_id
		 JOIN workflow_versions wv ON wv.id = t.workflow_version_id
		 WHERE wv.workflow_id = $1`, h.workflowID).Scan(&reposOnWorkflowA); err != nil {
		t.Fatal(err)
	}
	if reposOnWorkflowA < 2 {
		t.Fatalf("workflow A's runs span %d repos, want at least 2 — "+
			"a repo_id filter implemented as a workflow_id filter passes otherwise",
			reposOnWorkflowA)
	}

	// And the version half: the run staged on a second version really is on one
	// that is not what every other workflow-A task pins.
	var version int32
	if err := h.pool.QueryRow(ctx,
		`SELECT wv.version
		 FROM runs r JOIN tasks t ON t.id = r.task_id
		 JOIN workflow_versions wv ON wv.id = t.workflow_version_id
		 WHERE r.id = $1`, staged.onSecondVersion).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version == gatedVersion {
		t.Fatalf("the run staged on a second version is on version %d, the same one "+
			"every other task pins — a workflow filter that resolved a workflow to a "+
			"single version id would pass", version)
	}
	var distinctVersions int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(DISTINCT t.workflow_version_id)
		 FROM runs r JOIN tasks t ON t.id = r.task_id
		 JOIN workflow_versions wv ON wv.id = t.workflow_version_id
		 WHERE wv.workflow_id = $1`, h.workflowID).Scan(&distinctVersions); err != nil {
		t.Fatal(err)
	}
	if distinctVersions < 2 {
		t.Fatalf("workflow A's runs span %d versions, want at least 2", distinctVersions)
	}
}

// assertSharedPairStraddlesAPageBoundary is the half of property 1 that the
// duplicate count cannot see. A shared created_at only exercises the id half of
// the keyset predicate when a PAGE ENDS BETWEEN the two rows that carry it:
// that is the one moment a cursor is minted from a timestamp another run also
// has, and a cursor missing its id half either re-emits that run or skips it.
// A pair sitting wholly inside one page is invisible to the walk — the fixture
// stays "not the simple case" by the duplicate count and tests nothing new.
func (h *harness) assertSharedPairStraddlesAPageBoundary(t *testing.T) {
	t.Helper()
	rows, err := h.pool.Query(context.Background(),
		`SELECT created_at FROM runs ORDER BY created_at DESC, id DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ats []time.Time
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		ats = append(ats, at)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Rows sharing a created_at are adjacent under this ORDER BY, so one pass
	// over neighbours finds the pair.
	for i := 0; i+1 < len(ats); i++ {
		if !ats[i].Equal(ats[i+1]) {
			continue
		}
		if (i+1)%listPageWalkSize != 0 {
			t.Fatalf("the shared created_at pair sits at ranks %d and %d of %d, "+
				"which a %d-row walk keeps inside one page — no cursor is ever minted "+
				"from a timestamp two runs share, so the id half of the keyset goes untested. "+
				"Re-tune the agoMin values in stageRunsForListing.",
				i+1, i+2, len(ats), listPageWalkSize)
		}
		return
	}
	t.Fatalf("no two adjacent runs share a created_at over %d runs", len(ats))
}

// nodeCountOfWorkflow is the node count of the definition every staged task
// pins. It is read off the document the definition port actually serves, so a
// change to gatedDoc cannot leave the assertion pinning a stale number.
func (h *harness) nodeCountOfWorkflow(t *testing.T) int {
	t.Helper()
	for _, v := range h.versions {
		if v.number == gatedVersion {
			return len(v.doc.Nodes)
		}
	}
	t.Fatalf("no staged version %d", gatedVersion)
	return 0
}

func (h *harness) listRuns(t *testing.T, token, query string) runsapi.RunListPageResponse {
	t.Helper()
	resp := h.do(t, http.MethodGet, "/api/v1/runs"+query, token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/runs%s = %d, want 200", query, resp.StatusCode)
	}
	return decodeInto[runsapi.RunListPageResponse](t, resp)
}

func (h *harness) listRunsPaged(t *testing.T, token, cursor string, limit int) runsapi.RunListPageResponse {
	t.Helper()
	query := "?limit=" + strconv.Itoa(limit)
	if cursor != "" {
		query += "&cursor=" + cursor
	}
	return h.listRuns(t, token, query)
}

// allRunIDsNewestFirst is the unpaginated answer, read straight from the
// database with the query's own sort key. It is the set AND the order every
// paged walk has to reproduce.
func (h *harness) allRunIDsNewestFirst(t *testing.T) []string {
	t.Helper()
	rows, err := h.pool.Query(context.Background(),
		`SELECT id FROM runs ORDER BY created_at DESC, id DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id.String())
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSameSet(t *testing.T, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if len(g) != len(w) {
		t.Fatalf("paged walk saw %d runs, want %d", len(g), len(w))
	}
	for i := range g {
		if g[i] != w[i] {
			t.Fatalf("paged walk saw a different SET of runs:\n got %v\nwant %v", g, w)
		}
	}
}

func assertNoDuplicates(t *testing.T, got []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, id := range got {
		if seen[id] {
			t.Fatalf("the paged walk returned %s twice: %v", id, got)
		}
		seen[id] = true
	}
}

func assertEqualSlices(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("paged order has %d ids, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("paged order diverges at %d:\n got %v\nwant %v", i, got, want)
		}
	}
}

func TestListRuns(t *testing.T) {
	h := newHarness(t)
	staged := h.stageRunsForListing(t)

	t.Run("scoped exactly as the approvals inbox is", func(t *testing.T) {
		// The same rule, stated once per endpoint and asserted per endpoint:
		// a member sees the projects they hold ANY grant on and no others.
		a := h.listRuns(t, "approver-tok", "")
		if len(a.Runs) != 5 {
			t.Errorf("project-A approver got %d runs, want the 5 in their project", len(a.Runs))
		}
		for _, r := range a.Runs {
			if r.ProjectID != h.projectID.String() {
				t.Errorf("project-A approver got a run from %s", r.ProjectID)
			}
		}
		if len(h.listRuns(t, "outsider-tok", "").Runs) != 0 {
			t.Error("a member with no grants got runs")
		}
		if len(h.listRuns(t, "admin-tok", "").Runs) != 7 {
			t.Error("an admin does not see every run")
		}
	})

	t.Run("pages without dropping or repeating a row", func(t *testing.T) {
		// THE PROPERTY, and it is not "the page sizes are right". Walk the
		// whole list two rows at a time and assert the union is exactly the
		// unpaginated set, with no duplicates — which is what a cursor that
		// disagreed with the ORDER BY would break, and only on the rows that
		// share a created_at.
		var seen []string
		cursor := ""
		for page := 0; page < 10; page++ {
			got := h.listRunsPaged(t, "admin-tok", cursor, listPageWalkSize)
			for _, r := range got.Runs {
				seen = append(seen, r.ID)
			}
			if got.NextCursor == nil {
				break
			}
			cursor = *got.NextCursor
		}
		assertSameSet(t, seen, h.allRunIDsNewestFirst(t))
		assertNoDuplicates(t, seen)
		// And the ORDER survives paging, which a set comparison cannot see.
		assertEqualSlices(t, seen, h.allRunIDsNewestFirst(t))
	})

	t.Run("a card needs no second request", func(t *testing.T) {
		got := h.listRuns(t, "approver-tok", "")
		r := got.Runs[0]
		if r.ID != staged.newestInA.String() {
			t.Fatalf("newest project-A run = %s, want %s", r.ID, staged.newestInA)
		}
		if r.TaskTitle == "" || r.RepoSlug == "" || r.WorkflowName == "" || r.ProjectName == "" {
			t.Errorf("row is missing the labels a card renders: %+v", r)
		}
		if len(r.PhaseSummary) == 0 {
			t.Fatal("row carries no phase summary — the dots would need a fetch per run")
		}
		// Every node of the definition, not only the materialized ones.
		if len(r.PhaseSummary) != h.nodeCountOfWorkflow(t) {
			t.Errorf("phase_summary has %d entries, want one per node (%d)",
				len(r.PhaseSummary), h.nodeCountOfWorkflow(t))
		}
		for _, p := range r.PhaseSummary {
			if p.NodeType == "" {
				t.Errorf("phase %s carries no node_type; a client would have to guess from the id", p.NodeID)
			}
		}
		// A node with no row is NULL rather than "pending": pending is a real
		// status meaning "materialized and claimable", and conflating the two
		// tells the client a row exists that does not.
		byNode := map[string]*string{}
		for _, p := range r.PhaseSummary {
			byNode[p.NodeID] = p.Status
		}
		if byNode["impl"] == nil || *byNode["impl"] != "running" {
			t.Errorf("impl status = %v, want running", byNode["impl"])
		}
		if byNode["gate"] != nil {
			t.Errorf("unmaterialized gate status = %q, want null", *byNode["gate"])
		}
		// The run-level attempt is the newest attempt among its phase rows —
		// impl is on attempt 2 while plan is on 1.
		if r.Attempt != 2 {
			t.Errorf("run attempt = %d, want 2 (the newest phase attempt)", r.Attempt)
		}
		// And the other end of that rule: a run nothing has claimed reports 0,
		// not 1. "Attempt 1" on a queued run tells a card the first try is
		// already under way, and the fixture guard above holds this row empty.
		var unclaimed *runsapi.RunSummaryResponse
		for i := range got.Runs {
			if got.Runs[i].ID == staged.noPhases.String() {
				unclaimed = &got.Runs[i]
			}
		}
		if unclaimed == nil {
			t.Fatalf("the unclaimed run %s is not in the project-A list", staged.noPhases)
		}
		if unclaimed.Attempt != 0 {
			t.Errorf("unclaimed run attempt = %d, want 0 (no phase row has an ordinal yet)", unclaimed.Attempt)
		}
	})

	t.Run("filters", func(t *testing.T) {
		waiting := h.listRuns(t, "admin-tok", "?status=waiting")
		for _, r := range waiting.Runs {
			if r.Status != "waiting" {
				t.Errorf("status filter returned %s", r.Status)
			}
		}
		if len(waiting.Runs) == 0 {
			t.Error("the status filter matched nothing; the fixture must stage at least one waiting run or this asserts nothing")
		}
		if len(waiting.Runs) == len(h.listRuns(t, "admin-tok", "").Runs) {
			t.Error("the status filter returned the whole list; a filter that matches everything asserts nothing")
		}
		byTask := h.listRuns(t, "admin-tok", "?task_id="+staged.taskWithRuns.String())
		if len(byTask.Runs) == 0 {
			t.Error("the task filter matched nothing")
		}
		for _, r := range byTask.Runs {
			if r.TaskID != staged.taskWithRuns.String() {
				t.Errorf("task filter returned a run of task %s", r.TaskID)
			}
		}
	})

	t.Run("filters by repo and by workflow, and they compose", func(t *testing.T) {
		all := h.listRuns(t, "admin-tok", "").Runs
		ids := func(rs []runsapi.RunSummaryResponse) []string {
			out := make([]string, 0, len(rs))
			for _, r := range rs {
				out = append(out, r.ID)
			}
			return out
		}
		contains := func(rs []runsapi.RunSummaryResponse, id uuid.UUID) bool {
			for _, r := range rs {
				if r.ID == id.String() {
					return true
				}
			}
			return false
		}

		// REPO. The fixture puts runs of two workflows on repo A, so a proper
		// subset here is not explained by the workflow filter having been
		// wired to this parameter.
		byRepo := h.listRuns(t, "admin-tok", "?repo_id="+h.repoID.String()).Runs
		if len(byRepo) == 0 || len(byRepo) == len(all) {
			t.Errorf("repo filter returned %d of %d runs; a filter matching nothing or "+
				"everything asserts nothing", len(byRepo), len(all))
		}
		for _, r := range byRepo {
			if !contains(all, uuid.MustParse(r.ID)) {
				t.Errorf("repo filter returned %s, which the unfiltered list does not hold", r.ID)
			}
		}
		if contains(byRepo, staged.onSecondVersion) {
			t.Error("repo_id=A returned a run whose task is on repo B")
		}
		if !contains(byRepo, staged.newestInA) {
			t.Errorf("repo_id=A dropped %s, a run on repo A", staged.newestInA)
		}

		// WORKFLOW, and it is the workflow that decides, not the version. The
		// run on version 2 of workflow A has to come back, and the run on
		// workflow B must not.
		byWorkflow := h.listRuns(t, "admin-tok", "?workflow_id="+h.workflowID.String()).Runs
		if len(byWorkflow) == len(all) {
			t.Errorf("workflow_id=A returned the whole list (%d runs); the fixture stages "+
				"one run on a second workflow, so a proper subset is the answer", len(all))
		}
		if !contains(byWorkflow, staged.onSecondVersion) {
			t.Errorf("workflow_id=A dropped %s, whose task pins version 2 of that workflow — "+
				"the filter is on the WORKFLOW, and a task does not leave it by being pinned "+
				"to an older version", staged.onSecondVersion)
		}
		if contains(byWorkflow, staged.onWorkflowB) {
			t.Errorf("workflow_id=A returned %s, whose task is on workflow B", staged.onWorkflowB)
		}
		byWorkflowB := h.listRuns(t, "admin-tok", "?workflow_id="+h.workflowB.String()).Runs
		if len(byWorkflowB) != 1 || !contains(byWorkflowB, staged.onWorkflowB) {
			t.Errorf("workflow_id=B = %v, want exactly [%s]", ids(byWorkflowB), staged.onWorkflowB)
		}

		// COMPOSED, and the composition is narrower than either half. The
		// workflow-B run is on repo A, so repo A ∩ workflow A drops it while
		// neither filter alone does what the other does.
		both := h.listRuns(t, "admin-tok",
			"?repo_id="+h.repoID.String()+"&workflow_id="+h.workflowID.String()).Runs
		if len(both) >= len(byRepo) || len(both) >= len(byWorkflow) {
			t.Errorf("repo_id=A&workflow_id=A returned %d runs, want fewer than both "+
				"repo_id=A (%d) and workflow_id=A (%d) — otherwise one of the two "+
				"predicates is not being applied",
				len(both), len(byRepo), len(byWorkflow))
		}
		if contains(both, staged.onWorkflowB) {
			t.Error("repo_id=A&workflow_id=A returned the workflow-B run that sits on repo A")
		}

		// And with status, whose predicate is a different shape again. The
		// unfiltered `running` set spans both repos; scoped to repo A it is one
		// run, so a dropped repo predicate is visible as a count.
		running := h.listRuns(t, "admin-tok", "?status=running").Runs
		runningInA := h.listRuns(t, "admin-tok", "?status=running&repo_id="+h.repoID.String()).Runs
		if len(running) < 2 {
			t.Fatalf("the fixture stages %d running runs, want at least 2 across both repos — "+
				"composing status with repo_id asserts nothing otherwise", len(running))
		}
		if len(runningInA) != 1 || !contains(runningInA, staged.newestInA) {
			t.Errorf("status=running&repo_id=A = %v, want exactly [%s]",
				ids(runningInA), staged.newestInA)
		}
	})

	t.Run("a run scoped away by a grant is not reachable through a filter", func(t *testing.T) {
		// The filters narrow; they never widen. A project-A approver naming
		// project B's repository gets an empty page, not project B's runs —
		// which is what a filter applied INSTEAD of the grant predicate, rather
		// than alongside it, would return.
		if got := h.listRuns(t, "approver-tok", "?repo_id="+h.repoB.String()).Runs; len(got) != 0 {
			t.Errorf("project-A approver filtering to repo B got %d runs, want 0", len(got))
		}
		// Workflow A spans both projects, so this one is the sharper case: the
		// filter matches rows in both, and only the grant predicate excludes B's.
		got := h.listRuns(t, "approver-tok", "?workflow_id="+h.workflowID.String()).Runs
		for _, r := range got {
			if r.ProjectID != h.projectID.String() {
				t.Errorf("project-A approver filtering to workflow A got a run from project %s", r.ProjectID)
			}
		}
		if len(got) == 0 {
			t.Error("project-A approver filtering to workflow A got nothing; the fixture stages " +
				"their own runs on it, so this would assert nothing")
		}
	})

	t.Run("a bad cursor and a bad status are 400 with a field error, not 500", func(t *testing.T) {
		for _, tc := range []struct{ query, field string }{
			{"?cursor=!!!", "cursor"},
			{"?status=nonsense", "status"},
			{"?task_id=not-a-uuid", "task_id"},
			{"?workflow_id=not-a-uuid", "workflow_id"},
			{"?repo_id=not-a-uuid", "repo_id"},
			{"?limit=soon", "limit"},
		} {
			resp := h.do(t, http.MethodGet, "/api/v1/runs"+tc.query, "admin-tok", nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("GET /runs%s = %d, want 400", tc.query, resp.StatusCode)
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
				t.Errorf("GET /runs%s errors = %+v, want one blaming %q", tc.query, pd.Errors, tc.field)
			}
		}
	})

	t.Run("a page's next_cursor is null on the last page", func(t *testing.T) {
		// The distinction the client branches on. An empty-string cursor would
		// typecheck and loop forever; an absent key would make the property
		// optional in a contract that promises it.
		full := h.listRuns(t, "admin-tok", "")
		if full.NextCursor != nil {
			t.Errorf("a page holding every run carries next_cursor %q, want null", *full.NextCursor)
		}
		first := h.listRunsPaged(t, "admin-tok", "", 2)
		if first.NextCursor == nil {
			t.Fatal("a page with more runs behind it carries no next_cursor")
		}
	})

	t.Run("timestamps survive the projection", func(t *testing.T) {
		// started_at and finished_at are required AND nullable, and a card
		// renders finished_at ?? started_at ?? created_at — so a projection
		// that dropped either would silently date every row by its creation.
		var pending, finished int
		for _, r := range h.listRuns(t, "admin-tok", "").Runs {
			if r.StartedAt == nil {
				pending++
				continue
			}
			if r.FinishedAt != nil {
				finished++
				if !r.FinishedAt.After(*r.StartedAt) {
					t.Errorf("run %s finished at %v, before it started at %v", r.ID, r.FinishedAt, r.StartedAt)
				}
			}
			if !r.StartedAt.After(r.CreatedAt.Add(-time.Second)) {
				t.Errorf("run %s started at %v, before it was created at %v", r.ID, r.StartedAt, r.CreatedAt)
			}
		}
		if pending == 0 || finished == 0 {
			t.Errorf("fixture staged %d unstarted and %d finished runs, want at least one of each", pending, finished)
		}
	})
}
