//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	runsapi "github.com/giiorg/caesar/backend/internal/runs/api"
)

// TestWaitingGateCarriesItsEvidence is the anti-guessing test. Every case here
// is one the client cannot answer for itself, which is why the fields exist.
func TestWaitingGateCarriesItsEvidence(t *testing.T) {
	h := newHarness(t)

	t.Run("the examined artifact is the one DecideGate stamps, even when it is not the newest", func(t *testing.T) {
		// THE SHAPE OF THIS FIXTURE IS THE TEST. Stage a run whose gate
		// examines a PLAN produced by an early node, and then let a LATER node
		// produce a review_verdict — so "the newest artifact on the run" and
		// "the artifact this gate examines" are different rows. A projection
		// that guessed would pass on any simpler graph.
		run, gateNode := h.stagePlanGateWithLaterVerdict(t)

		got := h.onlyApproval(t, "approver-tok", run)
		// The oracle is DecideGate's own answer, taken from the decision it
		// records — one function resolving the artifact, so the screen and the
		// stamp cannot drift.
		resp := h.do(t, http.MethodPost, decisionPath(run, gateNode), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("approve: %d", resp.StatusCode)
		}
		stamped := h.examinedArtifactOfDecision(t, run, gateNode)
		if got.Evidence.ExaminedArtifactID != stamped {
			t.Errorf("projection examined %s, DecideGate stamped %s",
				got.Evidence.ExaminedArtifactID, stamped)
		}
		if got.Evidence.ExaminedArtifactType != runs.ArtifactKindPlan {
			t.Errorf("examined_artifact_type = %q, want plan", got.Evidence.ExaminedArtifactType)
		}
		// And it really was not the newest artifact on the run, or the sentence
		// above is about a fixture that cannot tell the two apart.
		if newest := h.newestArtifact(t, run); newest == stamped {
			t.Errorf("the fixture's newest artifact IS the examined one (%s); "+
				"a projection that guessed would pass", newest)
		}
	})

	t.Run("a gate with no review verdict says so rather than guessing", func(t *testing.T) {
		run, _ := h.stagePatchGateWithoutVerdict(t)
		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.SummaryArtifactID != nil {
			t.Errorf("summary_artifact_id = %v on a gate whose graph has no verdict node", *got.Evidence.SummaryArtifactID)
		}
		// And the KEY is still present. "Absent" and "null" are different
		// answers to a client, and only one of them is a value it can branch on.
		if !h.rawApprovalHasKey(t, "approver-tok", run, "summary_artifact_id") {
			t.Error("summary_artifact_id was omitted; it must be present and null")
		}
	})

	t.Run("a gate whose verdict node declared nothing says so too", func(t *testing.T) {
		// The OTHER way to have no summary: the graph binds a review_verdict
		// and the run has not produced one. Declaring artifacts is optional
		// (collect.go), so this is an ordinary state and not corruption — the
		// inbox has to answer it, not fail on it.
		run, _ := h.stageVerdictGateWithoutItsArtifact(t)
		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.SummaryArtifactID != nil {
			t.Errorf("summary_artifact_id = %v on a gate whose verdict node produced nothing",
				*got.Evidence.SummaryArtifactID)
		}
	})

	t.Run("a gate WITH a verdict names it, and names this generation's", func(t *testing.T) {
		run, _, gen2Verdict := h.staleVerdictThenRevision(t)
		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.SummaryArtifactID == nil {
			t.Fatalf("summary_artifact_id is null, want generation 2's verdict %s", gen2Verdict)
		}
		// Dereferenced: %v of the pointer prints an ADDRESS, which says nothing
		// about which artifact was named.
		if *got.Evidence.SummaryArtifactID != gen2Verdict {
			t.Errorf("summary_artifact_id = %s, want generation 2's verdict %s",
				*got.Evidence.SummaryArtifactID, gen2Verdict)
		}
	})

	t.Run("an examined artifact the run never produced is reported empty, not substituted", func(t *testing.T) {
		// decideGate refuses this gate as well, so the surface says the evidence
		// is missing. Substituting some other artifact would show a human one
		// thing and stamp another; failing the whole request would take the
		// inbox down over one unfinished run.
		run, node := h.stageGateWithoutItsExaminedArtifact(t)
		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.ExaminedArtifactID != "" {
			t.Errorf("examined_artifact_id = %q, want empty: the run produced none", got.Evidence.ExaminedArtifactID)
		}
		// The TYPE is still answered — it comes off the definition, which is
		// there — so the surface can say which kind of evidence is missing.
		if got.Evidence.ExaminedArtifactType != runs.ArtifactKindPatch {
			t.Errorf("examined_artifact_type = %q, want patch", got.Evidence.ExaminedArtifactType)
		}
		// And the caller is told NOT to try. approver-tok holds approve and did
		// not request this run, so both caller-scoped refusals pass — the only
		// thing standing between them and a 500 is this reason.
		if got.Evidence.CanDecide {
			t.Error("can_decide is true on a gate whose examined artifact does not exist")
		}
		if got.Evidence.CannotDecideReason != "evidence_missing" {
			t.Errorf("cannot_decide_reason = %q, want evidence_missing", got.Evidence.CannotDecideReason)
		}
		// The oracle, again: what decideGate really answers. can_decide is only
		// worth rendering if it predicts the endpoint, and a 500 is precisely
		// the answer a client must never be invited to provoke.
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode == http.StatusOK {
			t.Errorf("decideGate = %d on a gate with no examined artifact; "+
				"if it succeeds now, can_decide: false is the wrong answer", resp.StatusCode)
		}
	})

	t.Run("a caller who is both the requester and short of approve is told the fixable one", func(t *testing.T) {
		// Both caller-scoped refusals are true at once. The documented order
		// (api.go) answers not_approver, because a grant can be raised and the
		// requester of an existing run cannot stop being it. Without this case
		// the order is free to invert unnoticed.
		run, node := h.stageGateNoSelfApproval(t, "runner-tok") // run grant: below approve
		got := h.onlyApproval(t, "runner-tok", run)
		if got.Evidence.CanDecide {
			t.Error("can_decide is true for a caller who is neither an approver nor allowed to self-approve")
		}
		if got.Evidence.CannotDecideReason != "not_approver" {
			t.Errorf("cannot_decide_reason = %q, want not_approver (the grant is the fixable half)",
				got.Evidence.CannotDecideReason)
		}
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "runner-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("decideGate = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("a view-only member sees the gate and is told they may not decide it", func(t *testing.T) {
		run, node := h.stagePatchGateWithoutVerdict(t)
		got := h.onlyApproval(t, "viewer-tok", run)
		if got.Evidence.CanDecide {
			t.Error("can_decide is true for a principal decideGate refuses")
		}
		if got.Evidence.CannotDecideReason != "not_approver" {
			t.Errorf("cannot_decide_reason = %q, want not_approver", got.Evidence.CannotDecideReason)
		}
		// The projection must agree with the endpoint. A can_decide computed
		// from a different rule is worse than no can_decide at all.
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "viewer-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("decideGate = %d for a caller can_decide called undecidable", resp.StatusCode)
		}
	})

	t.Run("an admin may decide every gate, grants or no grants", func(t *testing.T) {
		// Authorize answers true for an admin whatever their grants say, and an
		// admin holds NONE — so a rule read off the grant map alone reports
		// not_approver on every row of the one inbox that sees every row.
		run, node := h.stagePatchGateWithoutVerdict(t)
		got := h.onlyApproval(t, "admin-tok", run)
		if !got.Evidence.CanDecide || got.Evidence.CannotDecideReason != "" {
			t.Errorf("admin evidence = %+v, want can_decide with no reason", got.Evidence)
		}
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "admin-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusOK {
			t.Errorf("decideGate = %d for the admin can_decide called able", resp.StatusCode)
		}
	})

	t.Run("the requester of a no-self-approval run is refused all three, not just approve", func(t *testing.T) {
		run, node := h.stageGateNoSelfApproval(t, "approver-tok") // and approver-tok is asking
		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.CanDecide {
			t.Error("can_decide is true for the requester of a run that forbids self-approval")
		}
		if got.Evidence.CannotDecideReason != "self_approval_forbidden" {
			t.Errorf("cannot_decide_reason = %q, want self_approval_forbidden", got.Evidence.CannotDecideReason)
		}
		// DecideGate refuses BEFORE branching on decision type (gates.go), so
		// all three really are refused. Asserting only approve is how a UI ends
		// up shipping two controls guaranteed to 403.
		for _, d := range []string{"approve", "request_changes", "reject"} {
			body := runsapi.GateDecisionRequest{Decision: d, Note: "n"}
			if resp := h.do(t, http.MethodPost, decisionPath(run, node), "approver-tok", body); resp.StatusCode != http.StatusForbidden {
				t.Errorf("decideGate %s = %d, want 403", d, resp.StatusCode)
			}
		}
	})

	t.Run("the same evidence rides the run's own gate phase", func(t *testing.T) {
		run, node := h.stagePatchGateWithoutVerdict(t)
		fromInbox := h.onlyApproval(t, "approver-tok", run).Evidence
		detail := h.getRun(t, "approver-tok", run)
		var fromPhase *runsapi.GateEvidenceResponse
		for _, p := range detail.Phases {
			if p.NodeID == node && p.Evidence != nil {
				fromPhase = p.Evidence
			}
		}
		if fromPhase == nil {
			t.Fatal("the run's waiting gate phase carries no evidence")
		}
		// cmp.Diff rather than !=: the struct holds a *string, and == would
		// compare two independently decoded JSON values by ADDRESS.
		if diff := cmp.Diff(fromInbox, *fromPhase); diff != "" {
			t.Errorf("phase evidence differs from inbox evidence (-inbox +phase):\n%s", diff)
		}
	})

	t.Run("a decided or non-gate phase carries none", func(t *testing.T) {
		run, node := h.stagePatchGateWithoutVerdict(t)
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusOK {
			t.Fatalf("approve: %d", resp.StatusCode)
		}
		for _, p := range h.getRun(t, "approver-tok", run).Phases {
			if p.Evidence != nil {
				t.Errorf("phase %s (status %s) carries evidence after the gate was decided", p.NodeID, p.Status)
			}
		}
	})
}

// TestDecidabilityPastTheWallDeadline is §5's FOURTH refusal, and every case
// here carries decideGate's own answer as its oracle. can_decide is only worth
// rendering if it predicts the endpoint: a gate the inbox calls decidable and
// the endpoint answers 409 ships a control guaranteed to fail, which is the one
// outcome this field exists to prevent.
func TestDecidabilityPastTheWallDeadline(t *testing.T) {
	h := newHarness(t)

	t.Run("an approver who could otherwise decide is told the deadline instead", func(t *testing.T) {
		// Every OTHER refusal passes on this fixture: approver-tok holds
		// approve, did not request the run, and the examined patch exists. So
		// can_decide is TRUE here without the deadline arm, which makes this
		// case the arm's own test rather than a restatement of one of the three.
		run, node := h.stagePatchGateWithoutVerdict(t)
		h.expireWallDeadline(t, run)

		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.CanDecide {
			t.Error("can_decide is true on a run whose wall deadline has passed")
		}
		if got.Evidence.CannotDecideReason != "deadline_expired" {
			t.Errorf("cannot_decide_reason = %q, want deadline_expired", got.Evidence.CannotDecideReason)
		}
		// The oracle. gates.go:176 refuses on the deadline with ClassConflict,
		// which httpx answers 409 — so a caller who followed can_decide: true
		// would have been sent to a certain failure.
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusConflict {
			t.Errorf("decideGate = %d, want 409", resp.StatusCode)
		}
	})

	t.Run("the deadline beats self_approval_forbidden, in decideGate's own order", func(t *testing.T) {
		// BOTH refusals are true, which is the only fixture in which the order
		// between them is observable at all — with either condition alone the
		// two arms are interchangeable. decideGate refuses on the deadline at
		// gates.go:176 before the policy at :189, so the deadline is the
		// refusal this caller would ACTUALLY receive.
		run, node := h.stageGateNoSelfApproval(t, "approver-tok")
		h.expireWallDeadline(t, run)

		got := h.onlyApproval(t, "approver-tok", run)
		if got.Evidence.CannotDecideReason != "deadline_expired" {
			t.Errorf("order violated: cannot_decide_reason = %q, want deadline_expired before self_approval_forbidden",
				got.Evidence.CannotDecideReason)
		}
		// And the endpoint really does answer the deadline's 409 rather than
		// the policy's 403. Without this, the ordering claim above is a claim
		// about this file rather than about the server.
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "approver-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusConflict {
			t.Errorf("decideGate = %d, want 409 (the deadline), not 403 (the policy)", resp.StatusCode)
		}
	})

	t.Run("not_approver still wins: the grant is checked before the deadline is read", func(t *testing.T) {
		// The deadline arm is SECOND and not first, and this is what holds it
		// there. handleDecideGate authorizes GrantApprove before DecideGate
		// opens its transaction, so a viewer on an expired run gets 403 and
		// never reaches gates.go:176 at all — naming the deadline would send
		// them after a fact that is true and not theirs to fix.
		run, node := h.stagePatchGateWithoutVerdict(t)
		h.expireWallDeadline(t, run)

		got := h.onlyApproval(t, "viewer-tok", run)
		if got.Evidence.CannotDecideReason != "not_approver" {
			t.Errorf("cannot_decide_reason = %q, want not_approver: the grant is checked first",
				got.Evidence.CannotDecideReason)
		}
		if resp := h.do(t, http.MethodPost, decisionPath(run, node), "viewer-tok",
			runsapi.GateDecisionRequest{Decision: "approve"}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("decideGate = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("the run's own gate phase says it too", func(t *testing.T) {
		// The SECOND read path, and it reads its own clock: run detail resolves
		// evidence through attachGateEvidence rather than through
		// ListWaitingGates. An arm threaded into the inbox alone would leave
		// this surface offering three controls that 409, and §2.4 is one
		// component with two hosts.
		run, node := h.stagePatchGateWithoutVerdict(t)
		h.expireWallDeadline(t, run)

		var fromPhase *runsapi.GateEvidenceResponse
		for _, p := range h.getRun(t, "approver-tok", run).Phases {
			if p.NodeID == node && p.Evidence != nil {
				fromPhase = p.Evidence
			}
		}
		if fromPhase == nil {
			t.Fatal("the run's waiting gate phase carries no evidence")
		}
		if fromPhase.CanDecide || fromPhase.CannotDecideReason != "deadline_expired" {
			t.Errorf("phase evidence = (%v, %q), want (false, deadline_expired)",
				fromPhase.CanDecide, fromPhase.CannotDecideReason)
		}
	})
}

// stagePlanGateWithLaterVerdict parks a run of planGateDoc at its gate: the
// gate examines the PLAN, and the review node's verdict — produced afterwards —
// is the newest artifact on the run.
func (h *harness) stagePlanGateWithLaterVerdict(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	// The runner requests it so the approver is not the requester; self-approval
	// is subject of its own fixture, not an accident of this one.
	task := h.createTaskOnVersion(t, "runner-tok", h.repoID, "a plan to decide", planGateVersion)
	runID := runIDOf(t, task)
	h.parkAtGate(t, runID, 1)
	h.stageArtifact(t, runID, "plan", runs.ArtifactKindPlan, 1, 1, 40, `{}`)
	h.stagePhase(t, runID, "plan", "succeeded", 1, 1, 45, mins(40))
	h.stageArtifact(t, runID, "review", runs.ArtifactKindReviewVerdict, 1, 1, 20, `{}`)
	h.stagePhase(t, runID, "review", "succeeded", 1, 1, 30, mins(20))
	h.stagePhase(t, runID, "gate", "waiting_decision", 1, 1, 10, nil)
	return runID, "gate"
}

// stagePatchGateWithoutVerdict is the shipped gatedDoc: a gate over a patch,
// with no review node anywhere in the graph. The runner requests it, so the
// approver may decide and the viewer may not.
func (h *harness) stagePatchGateWithoutVerdict(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	_, runID, _ := h.gatedRun(t, "runner-tok", h.repoID, "a patch to decide")
	return runID, "gate"
}

// staleVerdictThenRevision parks a run of verdictGateDoc at its SECOND
// generation's gate. Three review verdicts exist on the run and only one of
// them is this gate's: generation 1's is stale, and the advisory `late` node's
// is the newest row of that type on the whole run.
func (h *harness) staleVerdictThenRevision(t *testing.T) (uuid.UUID, string, string) {
	t.Helper()
	task := h.createTaskOnVersion(t, "runner-tok", h.repoID, "two rounds of review", verdictGateVersion)
	runID := runIDOf(t, task)
	baseSHA, _ := h.parkAtGate(t, runID, 2)

	h.stageArtifact(t, runID, "impl", runs.ArtifactKindPatch, 1, 1, 90, patchPayload(supersededTreeHash, baseSHA))
	h.stagePhase(t, runID, "impl", "superseded", 1, 1, 95, mins(90))
	h.stageArtifact(t, runID, "review", runs.ArtifactKindReviewVerdict, 1, 1, 85, `{}`)
	h.stagePhase(t, runID, "review", "superseded", 1, 1, 88, mins(85))
	h.stagePhase(t, runID, "gate", "superseded", 1, 1, 80, mins(78))

	h.stageArtifact(t, runID, "impl", runs.ArtifactKindPatch, 2, 2, 40, patchPayload(stagedTreeHash, baseSHA))
	h.stagePhase(t, runID, "impl", "succeeded", 2, 2, 45, mins(40))
	gen2Verdict := h.stageArtifact(t, runID, "review", runs.ArtifactKindReviewVerdict, 2, 2, 30, `{}`)
	h.stagePhase(t, runID, "review", "succeeded", 2, 2, 35, mins(30))
	// The advisory reviewer finishes LAST, so its verdict is the newest
	// review_verdict on the run and the gate consumes none of it.
	h.stageArtifact(t, runID, "late", runs.ArtifactKindReviewVerdict, 2, 2, 5, `{}`)
	h.stagePhase(t, runID, "late", "succeeded", 2, 2, 8, mins(5))
	h.stagePhase(t, runID, "gate", "waiting_decision", 3, 2, 3, nil)
	return runID, "gate", gen2Verdict.String()
}

// stageVerdictGateWithoutItsArtifact is staleVerdictThenRevision's graph with
// the review node having declared nothing — a legal outcome, and the second of
// the two ways a gate ends up with no summary.
func (h *harness) stageVerdictGateWithoutItsArtifact(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	task := h.createTaskOnVersion(t, "runner-tok", h.repoID, "a review that declared nothing", verdictGateVersion)
	runID := runIDOf(t, task)
	baseSHA, _ := h.parkAtGate(t, runID, 1)
	h.stageArtifact(t, runID, "impl", runs.ArtifactKindPatch, 1, 1, 40, patchPayload(stagedTreeHash, baseSHA))
	h.stagePhase(t, runID, "impl", "succeeded", 1, 1, 45, mins(40))
	h.stagePhase(t, runID, "review", "succeeded", 1, 1, 35, mins(30))
	h.stagePhase(t, runID, "gate", "waiting_decision", 1, 1, 10, nil)
	return runID, "gate"
}

// stageGateWithoutItsExaminedArtifact parks a run at a gate whose pinned
// definition binds a patch the run never produced.
func (h *harness) stageGateWithoutItsExaminedArtifact(t *testing.T) (uuid.UUID, string) {
	t.Helper()
	task := h.createTaskOnVersion(t, "runner-tok", h.repoID, "nothing to look at", gatedVersion)
	runID := runIDOf(t, task)
	h.parkAtGate(t, runID, 1)
	h.stagePhase(t, runID, "impl", "succeeded", 1, 1, 45, mins(40))
	h.stagePhase(t, runID, "gate", "waiting_decision", 1, 1, 10, nil)
	return runID, "gate"
}

// stageGateNoSelfApproval parks a gate on a run REQUESTED BY requester, under a
// policy that forbids deciding one's own gate. The requester is a parameter
// because which grant they hold is the whole difference between the two cases
// that read this fixture: an approver hits self_approval_forbidden, and a caller
// below approve hits not_approver first.
func (h *harness) stageGateNoSelfApproval(t *testing.T, requester string) (uuid.UUID, string) {
	t.Helper()
	_, runID, _ := h.gatedRun(t, requester, h.repoID, "my own gate")
	// The snapshot is pinned at intake from the shipped baseline, which permits
	// self-approval, and Plan 09 is what will make it configurable. Editing the
	// pinned document is how a run under the other policy is staged today; it is
	// the run's own copy, so nothing else is affected.
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE runs SET policy_snapshot = jsonb_set(policy_snapshot, '{allow_self_approval}', 'false')
		 WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
	return runID, "gate"
}

// expireWallDeadline moves a staged run's deadline one minute into the past.
//
// It is written off the DATABASE clock — `now() - interval '1 minute'` — and
// not off a Go time, for the same reason parkAtGate writes the FUTURE deadline
// that way: every deciding predicate compares against now(), and this harness's
// app clock is pinned to a fixed date in 2026. A deadline computed in Go would
// land on whichever side of the database's clock the two machines happened to
// put it, and every assertion below would be about the wrong refusal.
//
// wall_deadline is written once at insert and never updated by the system
// (gates.go:382), so rewriting the run's own copy is how an expired run is
// staged — the same technique stageGateNoSelfApproval uses on the snapshot.
func (h *harness) expireWallDeadline(t *testing.T, runID uuid.UUID) {
	t.Helper()
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE runs SET wall_deadline = now() - interval '1 minute' WHERE id = $1`, runID); err != nil {
		t.Fatal(err)
	}
}

// onlyApproval is the caller's inbox entry FOR THIS RUN, and fails unless there
// is exactly one. The inbox is scoped to a principal and a whole harness's
// worth of fixtures is waiting in it, so "the first entry" would be whichever
// run sorted earliest.
func (h *harness) onlyApproval(t *testing.T, token string, runID uuid.UUID) runsapi.ApprovalResponse {
	t.Helper()
	resp := h.do(t, http.MethodGet, "/api/v1/approvals", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inbox as %s = %d, want 200", token, resp.StatusCode)
	}
	var found []runsapi.ApprovalResponse
	for _, a := range decodeInto[runsapi.ApprovalListResponse](t, resp).Approvals {
		if a.RunID == runID.String() {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s's inbox has %d entries for run %s, want exactly one", token, len(found), runID)
	}
	return found[0]
}

// rawApprovalHasKey reports whether the run's inbox entry carries key inside
// its evidence object — the DOCUMENT, not the decoded struct, because a Go
// pointer field cannot tell an absent property from a null one.
func (h *harness) rawApprovalHasKey(t *testing.T, token string, runID uuid.UUID, key string) bool {
	t.Helper()
	resp := h.do(t, http.MethodGet, "/api/v1/approvals", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inbox as %s = %d, want 200", token, resp.StatusCode)
	}
	var body struct {
		Approvals []map[string]json.RawMessage `json:"approvals"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, a := range body.Approvals {
		var id string
		if err := json.Unmarshal(a["run_id"], &id); err != nil {
			t.Fatal(err)
		}
		if id != runID.String() {
			continue
		}
		evidence, ok := a["evidence"]
		if !ok {
			t.Fatalf("the inbox entry for %s carries no evidence at all", runID)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(evidence, &fields); err != nil {
			t.Fatal(err)
		}
		_, present := fields[key]
		return present
	}
	t.Fatalf("no inbox entry for run %s", runID)
	return false
}

func (h *harness) getRun(t *testing.T, token string, runID uuid.UUID) runsapi.RunResponse {
	t.Helper()
	resp := h.do(t, http.MethodGet, "/api/v1/runs/"+runID.String(), token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get run as %s = %d, want 200", token, resp.StatusCode)
	}
	return decodeInto[runsapi.RunResponse](t, resp)
}

// examinedArtifactOfDecision is what DecideGate STAMPED on the gate it decided,
// read back off the run projection. It is the oracle the evidence has to match,
// and it comes from the decision row rather than from the fixture's own
// bookkeeping so that a projection agreeing with the test but not with the
// decision path still fails.
func (h *harness) examinedArtifactOfDecision(t *testing.T, runID uuid.UUID, node string) string {
	t.Helper()
	var found []string
	for _, p := range h.getRun(t, "approver-tok", runID).Phases {
		if p.NodeID == node && p.Decision != nil {
			found = append(found, p.Decision.ExaminedArtifactID)
		}
	}
	if len(found) != 1 {
		t.Fatalf("gate %q of run %s carries %d decisions, want exactly one", node, runID, len(found))
	}
	return found[0]
}

// newestArtifact is the run's most recently created artifact of any type — the
// value a projection that guessed would most plausibly have used.
func (h *harness) newestArtifact(t *testing.T, runID uuid.UUID) string {
	t.Helper()
	var id uuid.UUID
	if err := h.pool.QueryRow(context.Background(),
		`SELECT id FROM artifacts WHERE run_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`,
		runID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id.String()
}

func runIDOf(t *testing.T, task runsapi.TaskResponse) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(task.LatestRunID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
