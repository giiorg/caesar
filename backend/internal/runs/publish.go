package runs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/pg"
	"github.com/giiorg/caesar/backend/internal/runs/store"
)

// publishStatus mirrors the publish_intents.status CHECK. It exists so the
// state machine branches on a typed value rather than on string literals
// scattered across three files.
type publishStatus string

const (
	publishPending   publishStatus = "pending"
	publishPushed    publishStatus = "pushed"
	publishPublished publishStatus = "published"
	publishHalted    publishStatus = "halted"
)

// publishTitleCap and publishBodyCap bound what reaches a forge. Both are
// generous compared to a task title (200 bytes) and exist so a pathological
// definition cannot make a request the forge rejects for size.
const (
	publishTitleCap = 250
	publishBodyCap  = 8 << 10
)

// authorization is the joined authorization the publish path works with: the
// approved_patches row plus the approver's email. Both store reads produce
// structurally identical rows under different generated names, so this is the
// one type the state machine passes around.
type authorization struct {
	store.ApprovedPatch
	ApproverEmail string
}

func authorizationFromValidRow(r store.GetValidAuthorizationForUpdateRow) authorization {
	return authorization{
		ApprovedPatch: store.ApprovedPatch{
			ID: r.ID, RunID: r.RunID, GateNodeID: r.GateNodeID, PublishNodeID: r.PublishNodeID,
			GateDecisionID: r.GateDecisionID, ArtifactID: r.ArtifactID, BaseSha: r.BaseSha,
			TreeHash: r.TreeHash, RepoID: r.RepoID, ForgeKind: r.ForgeKind, RepoSlug: r.RepoSlug,
			TargetBranch: r.TargetBranch, Generation: r.Generation, Status: r.Status,
			CreatedAt: r.CreatedAt, SettledAt: r.SettledAt,
		},
		ApproverEmail: r.ApproverEmail,
	}
}

func authorizationFromIDRow(r store.GetAuthorizationByIDRow) authorization {
	return authorization{
		ApprovedPatch: store.ApprovedPatch{
			ID: r.ID, RunID: r.RunID, GateNodeID: r.GateNodeID, PublishNodeID: r.PublishNodeID,
			GateDecisionID: r.GateDecisionID, ArtifactID: r.ArtifactID, BaseSha: r.BaseSha,
			TreeHash: r.TreeHash, RepoID: r.RepoID, ForgeKind: r.ForgeKind, RepoSlug: r.RepoSlug,
			TargetBranch: r.TargetBranch, Generation: r.Generation, Status: r.Status,
			CreatedAt: r.CreatedAt, SettledAt: r.SettledAt,
		},
		ApproverEmail: r.ApproverEmail,
	}
}

// derefText reads an optional text column. Every nullable field on
// publish_intents is a receipt that may not have landed yet, and the empty
// string is the honest rendering of "not yet". pgtype.Text's zero value
// already carries "" with Valid false, so this is a rename of .String that
// keeps the intent readable at the call sites.
func derefText(t pgtype.Text) string { return t.String }

// truncate cuts to at most n bytes without splitting a rune. The caps are
// generous enough that this only ever fires on pathological input, but a
// forge rejecting a request for size would surface as an unexplained
// permanent failure.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// emailLocalPart is the commit author's NAME (baked decision 7). caesar's
// users carry an email and nothing else, so the local part is the closest
// thing to a display name that exists; an address with no "@" is used whole.
func emailLocalPart(email string) string {
	if at := strings.IndexByte(email, '@'); at > 0 {
		return email[:at]
	}
	return email
}

// executePublish drives one git-publish attempt. It never calls the
// Executor: a publication is a control-plane effect with durable receipts,
// not something a container produces.
//
// The shape is: authorize (one tx, ends with the intent committed), push,
// receipt (one tx), open PR, receipt (one tx), then hand the result to the
// SAME evaluate the agentic path uses — so retries, exhaustion, budget and
// completion all keep exactly one implementation.
func (o *Orchestrator) executePublish(ctx context.Context, workerID string, cp cachedPlan, c claimed) {
	deadline := c.RunWallDeadline
	if rem := RemainingBudgetMS(c.BudgetMS, c.UsedMS); rem > 0 {
		if end := o.svc.clock.Now().Add(time.Duration(rem) * time.Millisecond); end.Before(deadline) {
			deadline = end
		}
	}
	execCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	hbDone := o.startHeartbeat(execCtx, cancel, c, workerID)
	res, err := o.runPublish(execCtx, cp, c)
	close(hbDone)
	o.evaluate(ctx, workerID, cp, c, PhaseRunning, res, err)
}

// intentRepo is the ONLY repository a publication addresses once its intent
// exists. Every probe, push and pull-request call goes through it.
//
// cp.repo is loaded from the assets row (orchestrator.go loadPlan), and that
// row is mutable: UpdateRepo can change a repo's slug, default branch and
// credential mid-run. So a publication resumed after a restart — or recovered
// after a crash, or settled after a deadline — would address whatever the
// repos table says NOW rather than what the approval named. The failure is
// quiet and bad in both directions: the effects land on the wrong repository,
// and recovery probes the wrong repository and concludes the real branch does
// not exist.
//
// authorizePublish revalidates cp.repo against the authorization before the
// intent is created (that is where a rename is caught and refused). After
// that point the intent's own columns are the authority, and cp.repo is not
// consulted again.
func intentRepo(intent store.PublishIntent) RepoRef {
	return RepoRef{
		ID:            intent.RepoID,
		Forge:         intent.ForgeKind,
		Slug:          intent.RepoSlug,
		DefaultBranch: intent.TargetBranch,
		CredentialID:  intent.CredentialID,
	}
}

// errIntentAdvanced says a receipt write found the intent already further
// along than this worker believed. It is never returned to the caller: the
// state machine reloads the row and continues from the real position.
//
// It exists because a lease takeover cannot retract a forge request already
// in flight. Two drivers can therefore be inside the same publication, and
// the loser's writes must lose without either failing the run or overwriting
// the winner's receipts.
var errIntentAdvanced = errors.New("runs: publish intent advanced concurrently")

// unchargedWhileUnfinished stops a publication in progress from spending its
// own failure budget.
//
// Both callers stand inside a live intent — 'pending' or 'pushed' — so a
// transient failure here is never "this work did not happen". It is "we could
// not see whether it happened", and the whole design answers that by asking
// again. But forge_unavailable and push_rejected are ordinary CHARGEABLE
// failures (Task 1), so a forge outage lasting max_attempts would exhaust the
// node and terminalize the run — with the branch, and possibly the pull
// request, already live on the forge and nothing left that will reconcile
// them. The budget exists to stop useless work; retrying here is not useless,
// it is the only thing that closes the publication.
//
// Only TRANSIENT outcomes are downgraded. A permanent one — a halted
// publication, a stale authorization — is a decision that repeats, and
// converting it to a retry would spin until the wall deadline instead of
// telling the truth now. The wall deadline remains the bound on this path,
// and expired-run settlement (Task 10) is what finishes the record if it hits.
//
// publish_not_started is downgraded too, for the OPPOSITE reason to the
// chargeable ones: it costs nothing already, but by the time it arrives HERE it
// is false, and the falsehood is a schedule. Both forge ports and the publisher
// type their own pre-flight failures with it (runsports.go notStarted: an
// unsupported forge string, a credential row that is gone, a DEK that will not
// unwrap, a full disk), and its schedule is {0, 0, 0} — an immediate requeue,
// correct for the case the code was minted for, where no intent exists and the
// successor authorizes from scratch. An intent DOES exist here, and every
// production cause above is persistent, so the same answer becomes claim →
// authorize → fail → requeue at now+0, uncharged and exempt from both
// terminalization doors, until the wall deadline: the run's execution budget
// spent at the maximum rate, and a credential.used audit row per lap. The
// honest non-start keeps its code and its immediate requeue by NOT coming
// through here — authorizePublish's exit types itself and returns directly.
func unchargedWhileUnfinished(err error) error {
	out := ClassifyOutcome(err)
	if out.Class != OutcomeTransient {
		return err
	}
	if !out.Chargeable() && out.Code != CodePublishNotStarted {
		return err
	}
	return OutcomeError{Outcome: Outcome{
		Class: OutcomeTransient, Code: CodePublishResumable,
		Message: fmt.Sprintf("%s while a publication is unfinished: %s", out.Code, out.Message),
		RetryAt: out.RetryAt, // a forge that stated its own reset still wins
	}}
}

// resumable types a failure that leaves a LIVE intent exactly where it was:
// transient, uncharged, and safe to ask again.
//
// Every caller stands inside an intent that is 'pending' or 'pushed', which is
// where ClassifyOutcome's default-deny (spec §2) is at its most dangerous: a
// raw pgx error becomes a PERMANENT internal_error, `evaluate` sends that
// straight to terminal failure, terminalizeRun supersedes every live phase,
// and no sweep revisits a failed run — so a branch already on the forge
// becomes an orphan. Naming the type once, here, is what keeps every exit of
// runPublish honest; the invariant on runPublish lists them.
func resumable(format string, args ...any) error {
	return OutcomeError{Outcome: Outcome{
		Class: OutcomeTransient, Code: CodePublishResumable,
		Message: fmt.Sprintf(format, args...),
	}}
}

// receiptFailure types the failure of a receipt transaction, and it is the
// difference between a run that resumes and a run that dies over a database
// blip with its branch already on the forge.
//
// The transaction runs AFTER its effect landed — the push, the pull request,
// or the decision to halt — so nothing would ever go back and finish writing
// it down if this returned a permanent answer.
//
// errIntentAdvanced is not a failure and passes through untouched: it means
// another driver's receipt won, which is a position to adopt, not an error.
func receiptFailure(intent store.PublishIntent, effect string, err error) error {
	if errors.Is(err, errIntentAdvanced) {
		return err
	}
	return resumable("%s receipt for %s did not commit: %v", effect, intent.Branch, err)
}

// notStartedUnlessTyped leaves a deliberate outcome alone and types everything
// else as an uncharged, resumable non-start. A stale authorization and a
// halted publication are real permanent answers and must survive; a raw pgx
// error is not an answer at all.
//
// Without it, every untyped failure inside the authorize transaction — nine
// statement errors and the COMMIT itself — would default-deny to a PERMANENT
// internal_error and terminalize a run that has caused nothing. This is the
// one place a publish failure is honestly "it never began", which is exactly
// what CodePublishNotStarted was minted for.
func notStartedUnlessTyped(err error) error {
	var oe OutcomeError
	if errors.As(err, &oe) {
		return err
	}
	return OutcomeError{Outcome: Outcome{
		Class: OutcomeTransient, Code: CodePublishNotStarted,
		Message: fmt.Sprintf("the publication could not be authorized: %v", err),
	}}
}

// probeFailure types the failure of a recovery PROBE — the branch probe that
// opens a push, and the pull-request listing that opens an open — and its whole
// content is that a verdict the adapter already reached SURVIVES.
//
// The forge port is the only layer that can see what a forge actually said:
// publishError (app) types permanent exactly what it can prove — a rejected
// credential as auth_expired, a repository it will not show as
// repo_unavailable — and leaves everything else transient on purpose. Both
// probes used to re-wrap that verdict as an unconditional transient
// forge_unavailable, which discarded the one thing it had proved. A revoked
// token then parked as a retry and re-probed until the wall deadline: no halt,
// no halt_reason, no publish.halted event, and a person told nothing while the
// pushed branch sat on the forge — an auth_expired presenting as a transient,
// which is the failure the halt set exists to prevent.
//
// Only an UNTYPED error is this function's to classify, and transient is the
// honest answer for one: a forge that failed in a way nobody has taught us to
// read has proved nothing, so ask again. The typed pass-through also carries
// RetryAt, so a rate limit that named its own reset is still obeyed rather than
// backed off on a guess.
//
// Typing is NOT deciding: a permanent verdict only halts because both call
// sites route it through maybeHalt, which is what turns a proven-permanent
// answer into a settled row instead of a run that dies over a live intent.
func probeFailure(subject string, err error) error {
	var oe OutcomeError
	if errors.As(err, &oe) {
		return err
	}
	return OutcomeError{Outcome: Outcome{
		Class: OutcomeTransient, Code: CodeForgeUnavailable,
		Message: fmt.Sprintf("probe %s: %v", subject, err),
	}}
}

// runPublish is the state machine proper.
//
// Every step reloads the intent when it loses a receipt CAS, so the loop is
// driven by the DATABASE's idea of the position rather than by this
// goroutine's. It cannot spin: each iteration either advances the status or
// returns, and the status only moves forward.
//
// ONE INVARIANT HOLDS THIS WHOLE DESIGN UP, and it is worth stating plainly
// because the recovery machinery in Task 10 is only sound if it is true:
//
//	Every error leaving this function with an intent still 'pending' or
//	'pushed' is TRANSIENT and UNCHARGED. A permanent answer only leaves
//	after it has SETTLED the intent — it halts, and the row records why.
//
// The second sentence is the part that was missing, and its absence was a
// defect rather than a wording gap: for a while this comment asserted the first
// sentence alone while nine permanent gitops exits returned raw, leaving a
// 'pending' row with no reason on a dead run. State the invariant in the form
// the code can actually keep, or the next permanent gets added without a halt
// for exactly the same reason the last nine were.
//
// Why it has to be an invariant rather than a tendency: `evaluate` terminalizes
// a run on a permanent outcome or on an exhausted failure budget, and
// terminalization is finished — terminalizeRun supersedes every live phase,
// FinishRun carries no deadline predicate, and a dead run is never claimed
// again. There is no sweep that revisits a FAILED run, only expired ones
// (ListExpiredRunIDs matches 'pending', 'running', 'waiting'). So a run that
// dies here dies with its branch on the forge and nothing that will ever look
// again — unless the row it dies over says so itself.
//
// With the invariant, the wall deadline becomes the only way THIS FUNCTION can
// terminalize over an UNSETTLED publication — and that path settles first
// (Task 10 Step 5). Four pieces enforce it, and each one is a place the
// invariant was broken before it was written down: receiptFailure types the
// transaction that commits an effect's receipt (the push's, the pull
// request's, and the halt's), resumable types every other read or write that
// happens with a live intent, unchargedWhileUnfinished stops a forge
// outage from spending the failure budget — and a pre-flight failure that says
// "nothing began" from requeuing with no delay when something has — and
// maybeHalt turns every permanent that reaches it into a settled row.
// Statuses 'published' and 'halted'
// are outside the invariant by definition: both are settled, so a permanent
// answer is honest.
//
// ONE EXCEPTION SURVIVES, and it is honest rather than tolerated: the
// authorizePublish leg. It runs before this function has an intent of its own,
// and notStartedUnlessTyped deliberately preserves the permanents it can
// raise — a stale or concurrently-spent authorization, and a marker that
// already carries a publication intent. Both roll their transaction back, so
// this attempt created nothing and has nothing half-done to strand. Everything
// untyped there becomes an uncharged publish_not_started instead.
//
// FOUR doors into this run's death are outside this function, and each is held
// shut by the thing named with it, because nothing here can see them. The list
// is an enumeration and not a proof of completeness: it is what Task 9 walked,
// and anything later found to terminalize a run over a live intent belongs in
// it — the fourth was found exactly that way, after this comment claimed three.
//
//   - the EXECUTION BUDGET. unchargedWhileUnfinished exempts these codes from
//     the node's FAILURE budget; the run's execution budget is a different
//     bound, pre-checked in evaluateFailure, and a breach there is demoted to
//     a permanent budget_exhausted. A publish attempt accrues real deltaMS (a
//     clone and a push), so a run that reaches its publish node nearly spent
//     could die exactly there — with its branch on the forge. Both publish
//     codes therefore join credential_busy in that pre-check's exemption
//     (orchestrator.go), on credential_busy's own stated reasoning: they
//     charge nothing, so demoting their requeue would kill a run for time it
//     never spent. The exemption is bounded and does not hand out free work —
//     the usage is still accrued, so once it crosses the budget the successor
//     stops satisfying ClaimPhase's predicate and the publication simply waits
//     for the wall deadline, where settlement finishes the record. That is the
//     whole trade: a run parked for a sweep that WILL come, instead of a run
//     terminalized 'failed', which no sweep revisits at all.
//
//   - the PARK-PAST-DEADLINE refusal, in the same function as that pre-check
//     and the cheapest of the four to reach: evaluateFailure will not park a
//     retry whose wake falls past the run's wall deadline, and terminalizes
//     instead. publish_resumable backs off ~15 seconds, so a single forge
//     hiccup that late in a run would kill it 'failed' with the branch pushed
//     and the deadline STILL IN THE FUTURE — the same orphan as the budget
//     door, reached with nothing exhausted at all. Both publish codes are
//     exempt from the refusal (exemptFromParkPastDeadline, outcome.go), and
//     the park is safe rather than a hole: ClaimPhase already carries
//     wall_deadline > now(), so the parked row is unclaimable either way. Same
//     trade as above — a run parked for a sweep that WILL come, instead of one
//     terminalized 'failed'.
//
//   - a lost lease. reconcileAttempt hands every other node kind to
//     Executor.Inspect and concludes CHARGEABLE reconcile_lost; a publish
//     attempt ran no container, so it is dispatched to inspectPublication
//     instead (orchestrator.go), which answers from the intent row and the
//     forge: publish_not_started, publish_resumable, the halted conflict, or
//     an adopted publication — never a charged loss.
//
//   - a cancelled context. ClassifyOutcome special-cases only
//     context.DeadlineExceeded, so a cancellation default-denies to a
//     permanent internal_error. It is unreachable in practice because the only
//     cancel is the heartbeat's, which fires exactly when this phase row is no
//     longer ours and the finishPhase CAS therefore drops the outcome — but
//     the invariant leans on that fencing CAS, not on the classification.
func (o *Orchestrator) runPublish(ctx context.Context, cp cachedPlan, c claimed) (ExecutionResult, error) {
	intent, auth, err := o.authorizePublish(ctx, cp, c)
	if err != nil {
		// Nothing has reached the forge yet, so this is the ONE place a
		// publish failure is honestly "it never began".
		return ExecutionResult{}, notStartedUnlessTyped(err)
	}
	// reload re-reads the durable position after a lost receipt CAS. The
	// winner's row is the truth; this worker adopts it rather than retrying
	// its own stale view.
	reload := func() error {
		row, rerr := store.New(o.svc.pool).GetPublishIntent(ctx, store.GetPublishIntentParams{
			RunID: c.Ref.RunID, NodeID: c.Ref.NodeID,
		})
		if rerr != nil {
			// Typed for the same reason receiptFailure is: this read happens
			// with an unfinished intent and a real effect already on the
			// forge, and a raw error would default-deny into a permanent
			// internal_error that kills the run over a failed SELECT.
			return resumable("reload publication position: %v", rerr)
		}
		intent = row
		return nil
	}

	if intent.Status == string(publishPending) {
		switch intent, err = o.pushPublication(ctx, cp, c, intent, auth); {
		case errors.Is(err, errIntentAdvanced):
			if err = reload(); err != nil {
				return ExecutionResult{}, err
			}
		case err != nil:
			return ExecutionResult{}, unchargedWhileUnfinished(err)
		}
	}
	if intent.Status == string(publishPushed) {
		switch intent, err = o.openPublication(ctx, cp, c, intent); {
		case errors.Is(err, errIntentAdvanced):
			if err = reload(); err != nil {
				return ExecutionResult{}, err
			}
		case err != nil:
			return ExecutionResult{}, unchargedWhileUnfinished(err)
		}
	}
	if intent.Status == string(publishHalted) {
		// Halted: either a prior attempt did it and this one arrived holding
		// the settled row, or a concurrent driver did it under us and reload
		// adopted their position. Nothing resumes a halt but a human, so this
		// is the same permanent answer every later attempt gets — and it is
		// the only thing standing between a halted intent and a SUCCESS
		// carrying a publication artifact with no pull request in it.
		return ExecutionResult{}, haltedOutcome(intent)
	}
	return ExecutionResult{Artifacts: []NewArtifact{publicationArtifact(intent)}}, nil
}

// haltedOutcome is the permanent answer a halted intent gives every later
// attempt. It is settled by definition, so a permanent class is honest here
// where it would be a defect one status earlier.
//
// It stays publish_conflict whatever CAUSED the halt, and that is deliberate:
// this answer is not a report of the cause, it is "this publication is halted
// and a human owns it" to an attempt that arrives afterwards. The causing code
// reaches the run through haltPublication, on the attempt that did the halting
// — which is also the attempt whose outcome terminalizes the run, since
// terminalization supersedes every remaining phase. Reporting the cause here
// too would mean reading it back off the intent, and the row has no column for
// it; the halt reason it does carry is prose, and re-deriving a code from prose
// is worse than the honest generic answer.
func haltedOutcome(intent store.PublishIntent) error {
	return OutcomeError{Outcome: Outcome{
		Class: OutcomePermanent, Code: CodePublishConflict,
		Message: "publication halted for manual reconciliation: " + derefText(intent.HaltReason),
	}}
}

// authorizePublish is the one transaction that turns an approval into a
// durable intention (spec §3 Publishing). It CAS-consumes the single-use
// ApprovedPatch, revalidating artifact id, base SHA, tree hash, repo/target
// and the run's wall-deadline predicate — a stale, mismatched, expired or
// already-consumed authorization aborts here, before any forge call.
//
// It is idempotent by construction: an intent that already exists is
// returned as-is and its authorization is already consumed, so a retry after
// a crash resumes rather than re-authorizes.
func (o *Orchestrator) authorizePublish(ctx context.Context, cp cachedPlan, c claimed) (store.PublishIntent, authorization, error) {
	var intent store.PublishIntent
	var auth authorization
	err := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		existing, err := q.GetPublishIntent(ctx, store.GetPublishIntentParams{
			RunID: c.Ref.RunID, NodeID: c.Ref.NodeID,
		})
		switch {
		case err == nil:
			intent = existing
			row, err := q.GetAuthorizationByID(ctx, existing.AuthorizationID)
			if err != nil {
				return err
			}
			auth = authorizationFromIDRow(row)
			return nil
		case !errorsIsNoRows(err):
			return err
		}

		// The claim proved the run was 'running'; nothing proves it still is.
		// A sibling node's permanent failure terminalizes the run to 'failed',
		// which no sweep revisits (ListExpiredRunIDs matches only pending,
		// running and waiting) — so an intent created past that point is an
		// orphan branch nobody will ever reconcile. ConsumeAuthorization's
		// predicate is the wall deadline alone and does not cover it.
		//
		// The row lock is free here: it also serialises this transaction
		// against a concurrent revision.
		runRow, err := q.GetRunForUpdate(ctx, c.Ref.RunID)
		if err != nil {
			return err
		}
		if RunStatus(runRow.Status) != RunRunning {
			return OutcomeError{Outcome: Outcome{
				Class: OutcomeTransient, Code: CodePublishNotStarted,
				Message: fmt.Sprintf("the run is %s; a publication may not begin against a run that is not running", runRow.Status),
			}}
		}

		valid, err := q.GetValidAuthorizationForUpdate(ctx, store.GetValidAuthorizationForUpdateParams{
			RunID: c.Ref.RunID, PublishNodeID: c.Ref.NodeID,
		})
		if errorsIsNoRows(err) {
			return OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodeAuthorizationStale,
				Message: fmt.Sprintf("no live approval authorizes %q; it was never granted, already spent, or invalidated by a newer generation", c.Ref.NodeID),
			}}
		}
		if err != nil {
			return err
		}
		auth = authorizationFromValidRow(valid)
		if err := o.revalidateAuthorization(ctx, q, cp, c, auth); err != nil {
			return err
		}
		requester, err := o.requesterEmail(ctx, cp)
		if err != nil {
			return err
		}

		now, err := q.DBNow(ctx)
		if err != nil {
			return err
		}
		// The predicate is inside the UPDATE, evaluated against the database
		// clock: an expired run fails it and no intent is created, so a
		// publish can never win a race against the deadline sweeper.
		n, err := q.ConsumeAuthorization(ctx, store.ConsumeAuthorizationParams{ID: auth.ID, SettledAt: &now})
		if err != nil {
			return err
		}
		if n == 0 {
			return OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodeAuthorizationStale,
				Message: "the authorization could not be consumed: it was spent concurrently, or the run's wall deadline has passed",
			}}
		}

		title, body := publicationText(cp, c, auth, requester)
		intent, err = q.CreatePublishIntent(ctx, store.CreatePublishIntentParams{
			RunID: c.Ref.RunID, NodeID: c.Ref.NodeID, Attempt: c.Ref.Attempt,
			AuthorizationID: auth.ID, RepoID: auth.RepoID,
			// Copied from the authorization, never re-resolved: the publish
			// path addresses the repository the human approved.
			ForgeKind: auth.ForgeKind, RepoSlug: auth.RepoSlug,
			// approved_patches carries no credential column, so this is the
			// repo's CURRENT credential rather than one the approval pinned.
			// Copying it here still buys the important half: every later
			// attempt and every recovery probe presents the SAME credential
			// this publication started with, even if UpdateRepo repoints the
			// repo afterwards.
			CredentialID: cp.repo.CredentialID,
			Branch:       PublicationBranch(auth.ArtifactID),
			TargetBranch: auth.TargetBranch, BaseSha: auth.BaseSha, TreeHash: auth.TreeHash,
			Title: title, Body: body,
			// Both snapshotted, never re-rendered: they are hashed into the
			// commit sha. See publish_intents.author_email / commit_message.
			AuthorEmail:   requester,
			CommitMessage: commitMessageFor(title, c.Ref, auth),
			CreatedAt:     now,
		})
		if isUniqueViolation(err) {
			// Either another worker created the intent between our read and
			// our insert, or the marker collides with a different run's
			// publication. Both are conflicts, and the second is one a human
			// must look at — so name what happened rather than guessing.
			return OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodePublishConflict,
				Message: fmt.Sprintf("a publication intent already exists for branch %s", PublicationBranch(auth.ArtifactID)),
			}}
		}
		if err != nil {
			return fmt.Errorf("runs: create publish intent: %w", err)
		}
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPublishIntentCreated,
			CorrelationID: c.Ref.String(),
			Payload: map[string]any{
				"node_id": c.Ref.NodeID, "attempt": c.Ref.Attempt,
				"authorization_id": auth.ID.String(), "artifact_id": auth.ArtifactID.String(),
				"branch": intent.Branch, "target_branch": intent.TargetBranch,
				"base_sha": intent.BaseSha, "tree_hash": intent.TreeHash,
			},
		}})
	})
	return intent, auth, err
}

// requesterEmail resolves the address this publication's commit is authored
// by, in the authorize transaction and nowhere else.
//
// It is deliberately NOT cached on cachedPlan. The address is hashed into the
// commit sha, so a value that can differ between two attempts of the same
// publication breaks the adopt comparison recovery depends on: the second
// attempt would rebuild a different commit and refuse a branch that is ours.
// Resolving it once, here, and writing it to publish_intents.author_email is
// what makes the rebuild reproducible.
func (o *Orchestrator) requesterEmail(ctx context.Context, cp cachedPlan) (string, error) {
	email, err := o.svc.principals.Email(ctx, cp.requesterID)
	if err != nil {
		// Nothing has happened yet, so an identity blip is a non-start rather
		// than a failure: uncharged, and the next attempt asks again.
		return "", OutcomeError{Outcome: Outcome{
			Class: OutcomeTransient, Code: CodePublishNotStarted,
			Message: fmt.Sprintf("resolve the requester %s for attribution: %v", cp.requesterID, err),
		}}
	}
	if email == "" {
		// A successful lookup that yields no address is our own data being
		// wrong, and it repeats. Committing as `<>` would publish an
		// unattributable commit, which is worse than refusing.
		return "", OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodeInternalError,
			Message: fmt.Sprintf("requester %s has no address; a publication cannot be attributed", cp.requesterID),
		}}
	}
	return email, nil
}

// revalidateAuthorization re-checks every field the approval pinned against
// what the run holds NOW. The gate wrote these values; a mismatch means
// something moved between approval and publication, and the answer is always
// to abort rather than to publish the newer thing.
func (o *Orchestrator) revalidateAuthorization(ctx context.Context, q *store.Queries,
	cp cachedPlan, c claimed, auth authorization,
) error {
	stale := func(format string, args ...any) error {
		return OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodeAuthorizationStale,
			Message: fmt.Sprintf(format, args...),
		}}
	}
	// Every coordinate the approval pinned, not just the id. A repo's slug and
	// default branch are mutable through UpdateRepo (assets/repos.go), so an
	// id that still matches proves nothing about WHERE this patch would land:
	// rename the repo mid-run and the id is unchanged while the push
	// destination is a different repository entirely.
	if auth.RepoID != cp.repo.ID {
		return stale("the approval was granted for repo %s but the run targets %s", auth.RepoID, cp.repo.ID)
	}
	if auth.ForgeKind != cp.repo.Forge || auth.RepoSlug != cp.repo.Slug {
		return stale("the approval was granted for %s:%s but that repository is now %s:%s; it moved after the approval",
			auth.ForgeKind, auth.RepoSlug, cp.repo.Forge, cp.repo.Slug)
	}
	if auth.TargetBranch != cp.repo.DefaultBranch {
		return stale("the approval targeted branch %q but the repository's default branch is now %q",
			auth.TargetBranch, cp.repo.DefaultBranch)
	}
	if auth.BaseSha != cp.baseSHA {
		return stale("the approval pinned base %s but the run pinned %s", auth.BaseSha, cp.baseSHA)
	}
	gate, ok := cp.plan.GateFor(c.Ref.NodeID)
	if !ok || gate != auth.GateNodeID {
		return stale("the approval came from gate %q, which does not authorize %q in this definition", auth.GateNodeID, c.Ref.NodeID)
	}
	art, err := q.GetArtifactByID(ctx, auth.ArtifactID)
	if err != nil {
		return fmt.Errorf("runs: approved artifact %s: %w", auth.ArtifactID, err)
	}
	payload, err := unmarshalMap(art.Payload)
	if err != nil {
		return err
	}
	if got, _ := payload["tree_hash"].(string); got != auth.TreeHash {
		return stale("the approved artifact's tree hash is %s but the approval recorded %s", got, auth.TreeHash)
	}
	// The patch the publish node CONSUMES must be the one that was approved.
	// If the definition routes a different artifact here, the approved thing
	// and the published thing would diverge, which is the exact failure the
	// gate-dominates-exact-patch invariant exists to prevent.
	for _, in := range cp.plan.Nodes[c.Ref.NodeID].Inputs {
		if in.Artifact != ArtifactKindPatch {
			continue
		}
		row, err := q.GetNodeArtifact(ctx, store.GetNodeArtifactParams{
			RunID: c.Ref.RunID, NodeID: in.Origin, Type: in.Artifact,
		})
		if err != nil {
			return fmt.Errorf("runs: consumed patch %s: %w", in.Origin, err)
		}
		if row.ID != auth.ArtifactID {
			return stale("this node consumes artifact %s but the approval authorized %s", row.ID, auth.ArtifactID)
		}
	}
	return nil
}

// publicationText renders the PR title and body. Everything here is
// provenance a human reading the PR needs to trace it back to the run and the
// approval.
func publicationText(cp cachedPlan, c claimed, auth authorization, requester string) (title, body string) {
	title = truncate(cp.taskTitle, publishTitleCap)
	if title == "" {
		title = fmt.Sprintf("caesar run %s", c.Ref.RunID)
	}
	body = truncate(fmt.Sprintf(
		"Requested by %s, approved by %s.\n\n"+
			"caesar run `%s`, node `%s`, generation %d.\n"+
			"Definition `%s`.\nBase `%s`.\n",
		requester, auth.ApproverEmail, c.Ref.RunID, c.Ref.NodeID,
		auth.Generation, cp.definitionHash, auth.BaseSha), publishBodyCap)
	return title, body
}

// pushPublication performs the git effect and commits its receipt. The order
// is load-bearing: the push happens, and only then is head_sha recorded. A
// crash in between leaves the intent 'pending' with a branch on the remote —
// which the NEXT attempt discovers through the recovery probe and adopts.
// Recording the receipt first would leave the opposite: a receipt for a push
// that never happened, which no probe can distinguish from a lost branch.
func (o *Orchestrator) pushPublication(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent, auth authorization,
) (store.PublishIntent, error) {
	// Recovery first: does the marker already exist, and is it ours?
	//
	// Through maybeHalt rather than returned raw, because this probe is the
	// FIRST thing a publication does: a credential revoked mid-run is refused
	// here, before the push it guards, so this is where the halt set's
	// auth_expired and repo_unavailable actually arrive. Returning them raw
	// would leave the intent 'pending' and terminalize the run with no
	// halt_reason for the person who has to reissue the token.
	expect, err := o.recoverBranch(ctx, intent)
	if err != nil {
		return o.maybeHalt(ctx, cp, c, intent, err)
	}

	art, err := store.New(o.svc.pool).GetArtifactByID(ctx, auth.ArtifactID)
	if err != nil {
		// The intent is LIVE and its authorization is already consumed, so a
		// raw error here would default-deny to a permanent internal_error and
		// kill the publication over a failed SELECT — with nothing left that
		// could re-authorize it. Asking again is the whole answer.
		return intent, resumable("read the approved artifact %s for %s: %v",
			auth.ArtifactID, intent.Branch, err)
	}
	diff, ok := artifactDiff(art)
	if !ok {
		// The plan-07 contract: a patch artifact's payload["diff"] is the
		// base64 canonical diff. Missing or non-string is a permanently
		// invalid artifact, never an empty publication.
		//
		// Through maybeHalt, not returned raw: this is the one permanent this
		// function raises itself, and it is reachable on a RESUMED attempt with
		// the branch already pushed. Returning it raw left the intent 'pending'
		// forever on a run no sweep revisits.
		return o.maybeHalt(ctx, cp, c, intent, OutcomeError{Outcome: Outcome{
			Class: OutcomePermanent, Code: CodeInvalidOutput,
			Message: fmt.Sprintf("approved artifact %s carries no diff", auth.ArtifactID),
		}})
	}

	res, err := o.svc.publisher.PushPatch(ctx, PushRequest{
		Ref: c.Ref, Repo: intentRepo(intent), Branch: intent.Branch, BaseSHA: intent.BaseSha,
		TreeHash: intent.TreeHash, Patch: diff,
		// Replayed from the intent, never re-rendered: see the comment on
		// AuthorName below — the message carries the APPROVER's address, which
		// is joined from the same mutable users row.
		Message: intent.CommitMessage,
		// The author identity is REPLAYED from the intent, never re-resolved
		// through PrincipalSource: the commit sha hashes it, so an address
		// that changed between two attempts would rebuild a DIFFERENT commit,
		// miss the adopt comparison, and turn a crash-after-push into a
		// permanent publish_conflict on a branch that is genuinely ours.
		AuthorName: emailLocalPart(intent.AuthorEmail), AuthorEmail: intent.AuthorEmail,
		// The intent's created_at, never the wall clock: it is the same value
		// on every attempt, so the rebuilt commit hashes identically and the
		// adopt path can actually match.
		Date:       intent.CreatedAt,
		ExpectHead: expect, Deadline: c.RunWallDeadline,
	})
	if err != nil {
		return o.maybeHalt(ctx, cp, c, intent, err)
	}

	now := o.svc.clock.Now()
	kind := EventPublishPushed
	if !res.Created {
		kind = EventPublishAdopted
	}
	err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.RecordPushReceipt(ctx, store.RecordPushReceiptParams{
			ID: intent.ID, HeadSha: pgtype.Text{String: res.HeadSHA, Valid: true}, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			// Somebody moved this intent past 'pending' while we were inside
			// the push — a lease takeover, or the expired-run settlement. The
			// receipt is already written and ours would be a regression, so
			// adopt theirs. Reloading rather than failing is what keeps a
			// takeover from charging a failure for work that succeeded.
			return errIntentAdvanced
		}
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: kind,
			CorrelationID: c.Ref.String(),
			Payload: map[string]any{
				// "branch" is what tells the two publish.adopted meanings
				// apart: this one adopted a BRANCH, openPublication's adopted
				// a pull request and carries "pr_number".
				"node_id": c.Ref.NodeID, "branch": intent.Branch,
				"head_sha": res.HeadSHA, "tree_hash": res.TreeHash, "created": res.Created,
			},
		}})
	})
	if err != nil {
		return intent, receiptFailure(intent, "push", err)
	}
	intent.Status = string(publishPushed)
	intent.HeadSha = pgtype.Text{String: res.HeadSHA, Valid: true}
	return intent, nil
}

// recoverBranch asks the forge what the deterministic marker points at
// (spec §3: "recovery queries the forge for the deterministic markers") and
// returns what this attempt should ASSERT it last saw.
//
// It deliberately does NOT return an unrecognised head. An earlier shape did,
// handing the observed sha down as the push's lease value — which quietly
// turned "somebody else's branch" into "a branch I may replace". Ownership is
// proved by rebuilding the commit and comparing shas, which the publisher does
// with the pinned date; this function's job is only to fail fast on a
// disagreement we can already see, and to stay silent otherwise.
//
// The receipt branch below is unreachable and that is not an oversight:
// pushPublication only runs while the intent is 'pending', and
// RecordPushReceipt is the sole writer of head_sha — it sets status='pushed'
// in the same statement. So intent.HeadSha.Valid is always false here, and the
// only live legs are the probe's error and its "no such branch".
//
// Task 10's settlement does not change that, though an earlier note here said
// it would: settlement never calls this function. It proves ownership itself,
// through ExpectedHead, precisely because it may not re-drive the state
// machine. The branch stays as the statement of an invariant this function
// must not assume away.
func (o *Orchestrator) recoverBranch(ctx context.Context, intent store.PublishIntent) (string, error) {
	head, ok, err := o.svc.forge.BranchHead(ctx, intentRepo(intent), intent.Branch)
	if err != nil {
		return "", probeFailure("branch "+intent.Branch, err)
	}
	if !ok {
		return "", nil
	}
	if intent.HeadSha.Valid {
		// We hold a receipt, so we know exactly what should be there.
		if intent.HeadSha.String != head {
			return "", OutcomeError{Outcome: Outcome{
				Class: OutcomePermanent, Code: CodePublishConflict,
				Message: fmt.Sprintf("branch %s is at %s but this publication recorded %s", intent.Branch, head, intent.HeadSha.String),
			}}
		}
		return head, nil
	}
	// No receipt: the branch may be ours from a lost push, or somebody
	// else's entirely, and nothing here can tell them apart. Assert nothing
	// and let the publisher's rebuilt-sha comparison give the exact answer —
	// adopt on a match, halt on anything else.
	return "", nil
}

// artifactDiff pulls the base64 canonical diff out of a patch artifact. Shared
// with Task 10's settlement rebuild for the same reason commitMessageFor is:
// the Patch bytes are commit input, so two readings of one artifact would be
// two different commits.
func artifactDiff(art store.Artifact) (string, bool) {
	payload, err := unmarshalMap(art.Payload)
	if err != nil {
		return "", false
	}
	diff, _ := payload["diff"].(string)
	return diff, diff != ""
}

// commitMessageFor renders the commit message ONCE, inside the authorize
// transaction, and the result is stored on the intent.
//
// It is rendered once rather than per attempt because the message is hashed
// into the commit sha and it interpolates the APPROVER's address — joined from
// the mutable users row, exactly like the author's. An address that changed
// between two attempts of one publication would rebuild a different commit,
// miss the adopt comparison, and turn a crash-after-push into a permanent
// publish_conflict on a branch that is genuinely ours.
//
// Storing the finished string also removes the drift risk the other way: Task
// 10's settlement rebuild reads publish_intents.commit_message, so there is no
// second renderer that could disagree with what was pushed.
//
// The attempt ordinal is deliberately absent: it differs between attempts of
// one publication, and every input here must not.
func commitMessageFor(title string, ref AttemptRef, auth authorization) string {
	return fmt.Sprintf("%s\n\ncaesar-run: %s\ncaesar-node: %s\ncaesar-artifact: %s\ncaesar-approved-by: %s\n",
		title, ref.RunID, ref.NodeID, auth.ArtifactID, auth.ApproverEmail)
}

// openPublication opens the pull request and commits its receipt. The FIND
// comes first, unconditionally: a crash between an open and its receipt is
// indistinguishable from never having opened, and asking is the only way to
// tell. A forge that answers "yes, here it is" turns a lost receipt into a
// found one; opening blind would leave two pull requests for one approval.
func (o *Orchestrator) openPublication(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent,
) (store.PublishIntent, error) {
	candidates, err := o.svc.forge.FindPullRequests(ctx, intentRepo(intent), intent.Branch)
	if err != nil {
		return o.maybeHalt(ctx, cp, c, intent, probeFailure("pull requests on "+intent.Branch, err))
	}
	pr, found, ambiguous := matchPublication(candidates, intent)
	if ambiguous {
		// More than one request on our marker could be ours, or one that
		// should be ours is missing the fields that would prove it. Either
		// way nothing here can pick correctly, and picking wrong records a
		// stranger's pull request as this run's publication.
		return o.haltPublication(ctx, cp, c, intent, CodePublishConflict, fmt.Sprintf(
			"%d pull requests on branch %s and none is provably this publication's",
			len(candidates), intent.Branch))
	}
	adopted := found
	if !found && len(candidates) > 0 {
		// Requests exist on our marker and none of them is ours. Opening
		// another would leave two, so a human decides.
		return o.haltPublication(ctx, cp, c, intent, CodePublishConflict, fmt.Sprintf(
			"branch %s already carries %d pull request(s), none matching this publication",
			intent.Branch, len(candidates)))
	}
	if !found {
		pr, err = o.svc.forge.OpenPullRequest(ctx, intentRepo(intent), PullRequestSpec{
			Head: intent.Branch, Base: intent.TargetBranch, Title: intent.Title, Body: intent.Body,
		})
		switch {
		case err != nil && ClassifyOutcome(err).Code == CodePublishConflict:
			// The forge refuses because a request for this head ALREADY
			// EXISTS — and the find above said there was none. Between those
			// two calls somebody opened it, and the only somebody that can
			// is another driver of this same intent: a lease takeover cannot
			// retract a request already on the wire, so two drivers can be
			// inside this publication at once (decision 20).
			//
			// Halting here is the trap. The winner has created the pull
			// request but may not have committed its receipt yet, so the
			// intent is still 'pushed' and HaltPublishIntent would SUCCEED —
			// excluding only 'published' fences the window after the receipt,
			// never the window before it. A human would then be told to
			// reconcile a publication that in fact succeeded, and the run
			// would fail with its pull request open on the forge.
			//
			// So the conflict is a recovery condition, not a verdict: ask
			// again, and let the answer decide.
			pr, err = o.adoptConflictingPublication(ctx, cp, c, intent)
			if err != nil {
				return intent, err
			}
			adopted = true
		case err != nil:
			return o.maybeHalt(ctx, cp, c, intent, err)
		}
	}

	now := o.svc.clock.Now()
	kind := EventPublishPROpened
	if adopted {
		kind = EventPublishAdopted
	}
	err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.RecordPRReceipt(ctx, store.RecordPRReceiptParams{
			ID:        intent.ID,
			PrNumber:  pgtype.Int8{Int64: pr.Number, Valid: true},
			PrUrl:     pgtype.Text{String: pr.URL, Valid: true},
			UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return errIntentAdvanced
		}
		return appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: kind,
			CorrelationID: c.Ref.String(),
			Payload: map[string]any{
				"node_id": c.Ref.NodeID, "branch": intent.Branch,
				"pr_number": pr.Number, "pr_url": pr.URL, "state": pr.State, "adopted": adopted,
			},
		}})
	})
	if err != nil {
		return intent, receiptFailure(intent, "pull request", err)
	}
	intent.Status = string(publishPublished)
	intent.PrNumber = pgtype.Int8{Int64: pr.Number, Valid: true}
	intent.PrUrl = pgtype.Text{String: pr.URL, Valid: true}
	return intent, nil
}

// matchPublication picks the ONE pull request that is provably this
// publication's, out of everything sitting on our marker.
//
// A match must agree on all three coordinates: head branch (implied — that is
// what was queried), target base, and head commit. Anything less is a guess:
// a marker can carry several closed requests and requests to other bases, and
// adopting the wrong one records a stranger's pull request as this run's
// output.
//
// A candidate MISSING a field it needs is not a match either. Treating an
// empty Base or HeadSHA as "no disagreement" inverts the burden of proof —
// absent evidence would become permission — so those count toward ambiguous
// and the caller halts.
//
// ambiguous is true when more than one candidate matches, or when a candidate
// could neither be confirmed nor ruled out.
func matchPublication(candidates []PullRequestRef, intent store.PublishIntent) (pr PullRequestRef, found, ambiguous bool) {
	for _, cand := range candidates {
		switch {
		case cand.Base == "" || cand.HeadSHA == "":
			return PullRequestRef{}, false, true // unprovable either way
		case cand.Base != intent.TargetBranch:
			continue // definitively not ours
		case !intent.HeadSha.Valid || cand.HeadSHA != intent.HeadSha.String:
			continue
		}
		if found {
			return PullRequestRef{}, false, true // two exact matches is not possible; do not choose
		}
		pr, found = cand, true
	}
	return pr, found, false
}

// maybeHalt settles the intent when the answer is PERMANENT and leaves every
// other error alone. Halting is durable and terminal: a human reconciles it,
// and no later attempt resumes it (spec §3: "halts for manual reconciliation
// on mismatch").
//
// It decides on the CLASS, and it used to decide on an allow-list of three
// codes (publish_conflict, auth_expired, repo_unavailable). That list was the
// wrong question asked well. It asked "is this a person's problem?" — and the
// honest answer for a bad patch or a lost base sha is "not really" — when the
// question this position forces is "must this publication's row be closed?",
// whose answer with a live intent is always yes.
//
// The cost of the old shape was not theoretical. gitops types NINE permanent
// exits (eight invalid_output, one base_sha_unavailable) and none of them was
// on the list, so each returned raw: the run terminalized 'failed', the intent
// stayed 'pending' with halt_reason NULL, no publish.halted event and no audit
// row — and ListExpiredRunIDs matches only 'pending'/'running'/'waiting' runs,
// so nothing ever looked again. On a RESUMED publication the branch was already
// on the forge, which is the orphan runPublish's invariant exists to forbid.
// An allow-list is fail-OPEN by construction: this defect is exactly what
// happens when someone adds a permanent code and does not think of this list.
//
// Deciding by class costs nothing that was working. A permanent outcome
// terminalizes the run either way — unchargedWhileUnfinished downgrades
// transients only — so this never converts a live run into a halt. The whole
// delta is whether the record says why. It also means an UNTYPED error, which
// ClassifyOutcome default-denies to a permanent internal_error, now settles the
// row instead of leaving it in flight; the run was dying on that either way, so
// a reason is strictly more than nothing.
//
// The three codes the old list named are still the ones an operator most often
// meets here, and they are still the ones worth knowing by sight:
// publish_conflict (the marker is in a state we cannot prove is ours),
// auth_expired (the credential was rejected; somebody must reissue it) and
// repo_unavailable (the forge will not show the repository). They now halt
// because they are permanent, not because they are listed.
func (o *Orchestrator) maybeHalt(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent, err error,
) (store.PublishIntent, error) {
	out := ClassifyOutcome(err)
	if out.Class != OutcomePermanent {
		return intent, err
	}
	// out.Code, not a constant: this is the one call site whose halt can be
	// caused by more than one thing, and the cause is what an operator acts on.
	return o.haltPublication(ctx, cp, c, intent, out.Code, out.Message)
}

// adoptConflictingPublication re-asks the forge after a create-PR conflict and
// returns the request this publication should record.
//
// It exists because "already exists" is an answer about the WORLD, not about
// who is entitled to it, and the entitled party is very often us — a second
// driver of this same intent that got there first. The re-query is what
// distinguishes the two, and it is safe to repeat: the find is read-only, and
// nothing here opens anything.
//
// Three answers, and only one of them is a human's problem:
//
//   - an exact match  → ours, opened by our other driver. Adopt it; the
//     receipt CAS then decides which driver records it, and the loser reloads
//     the winner's row.
//   - candidates, none provably ours → the marker really does carry a
//     stranger's request. Halt, exactly as the pre-create check would have.
//   - nothing visible → the forge told us a request exists and then did not
//     show it: read-after-write lag, or a replica behind the write. Guessing
//     either way is wrong, so resume — UNCHARGED, because a publication whose
//     branch is already pushed must not spend its budget on the forge's
//     eventual consistency.
func (o *Orchestrator) adoptConflictingPublication(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent,
) (PullRequestRef, error) {
	candidates, ferr := o.svc.forge.FindPullRequests(ctx, intentRepo(intent), intent.Branch)
	if ferr != nil {
		return PullRequestRef{}, resumable("re-probe after create conflict on %s: %v", intent.Branch, ferr)
	}
	pr, found, ambiguous := matchPublication(candidates, intent)
	switch {
	case found:
		return pr, nil
	case ambiguous || len(candidates) > 0:
		_, herr := o.haltPublication(ctx, cp, c, intent, CodePublishConflict, fmt.Sprintf(
			"forge refused a second pull request on branch %s and none of the %d present is provably this publication's",
			intent.Branch, len(candidates)))
		return PullRequestRef{}, herr
	default:
		return PullRequestRef{}, resumable("forge reports a pull request on %s that it does not yet list", intent.Branch)
	}
}

// haltPublication settles the intent for a human and answers with the code
// that CAUSED the halt.
//
// The code is a parameter rather than a constant because the halt set grew.
// While publish_conflict was the only answer that halted, stamping it here was
// true by construction; now a revoked token halts as auth_expired and a
// vanished repository as repo_unavailable, and those are the strings an
// operator greps and a UI or an alert keys on. The halt REASON names the cause
// in prose, but prose is not a field anything can match on — discarding the
// code would leave "the credential is dead" indistinguishable from "somebody
// else's branch is on our marker" everywhere except a human's reading of a
// sentence.
func (o *Orchestrator) haltPublication(ctx context.Context, cp cachedPlan, c claimed,
	intent store.PublishIntent, code, reason string,
) (store.PublishIntent, error) {
	now := o.svc.clock.Now()
	txErr := pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.HaltPublishIntent(ctx, store.HaltPublishIntentParams{
			ID: intent.ID, HaltReason: pgtype.Text{String: reason, Valid: true}, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			// The intent reached 'published' (or was already halted) while we
			// were deciding to halt it. A stale worker losing a create-PR race
			// lands here, and it must not be able to tell a human to reconcile
			// a publication that in fact succeeded.
			return errIntentAdvanced
		}
		if err := appendEvents(ctx, tx, now, []Event{{
			RunID: c.Ref.RunID, ProjectID: cp.projectID, Kind: EventPublishHalted,
			CorrelationID: c.Ref.String(),
			Payload:       map[string]any{"node_id": c.Ref.NodeID, "branch": intent.Branch, "reason": reason},
		}}); err != nil {
			return err
		}
		return o.svc.audit.Record(ctx, tx, audit.Entry{
			Action: "publish.halted", SubjectKind: "publish_intent", SubjectID: intent.ID.String(),
			Detail: map[string]any{
				"run_id": c.Ref.RunID.String(), "node_id": c.Ref.NodeID,
				"branch": intent.Branch, "reason": reason,
			},
		})
	})
	if txErr != nil {
		// A halt is a receipt like the other two, and the worst-placed one:
		// three of its four callers stand on a 'pushed' intent, so the branch
		// is already live on the forge. Returning txErr raw would default-deny
		// to a permanent internal_error and terminalize the run over a failed
		// COMMIT, leaving that branch orphaned and the halt unrecorded — the
		// exact outcome the invariant exists to forbid.
		//
		// Re-running the halt is convergent, which is what makes a transient
		// answer honest: a second call against an already-halted row matches
		// no rows, returns errIntentAdvanced, and the state machine reloads
		// and reports haltedOutcome.
		return intent, receiptFailure(intent, "halt", txErr)
	}
	intent.Status = string(publishHalted)
	return intent, OutcomeError{Outcome: Outcome{
		Class: OutcomePermanent, Code: code, Message: reason,
	}}
}

// ---------------------------------------------------------------------------
// Reconciliation and settlement: the two sweeps that meet a half-finished
// publication, and the opposite answers they need.
//
// The LEASE sweep resumes one — the run is alive, so re-driving the state
// machine is correct — and inspectPublication is how it reads the durable
// position. The WALL-DEADLINE sweep must not resume anything: the run has no
// authority left to cause anything, so settleRunPublications only asks the
// forge what already exists and writes the answer down.
// ---------------------------------------------------------------------------

const (
	// publishSettleGrace is how long a marker must stay missing before absence
	// is an answer. It bounds how long a request can be in flight after its
	// issuer was fenced — a property of HTTP, not an operator preference —
	// hence a constant rather than config.
	publishSettleGrace = 30 * time.Second
	// publishSettleProbeBudget bounds ONE ownership rebuild. It is not the
	// wait: ExpectedHead clones and applies, and settlement runs on the
	// janitor's context, which has no deadline of its own.
	publishSettleProbeBudget = 2 * time.Minute
	// publishSettleUnverifiableAfter is the give-up bound on a publication
	// settlement cannot verify. Deferring on an unreachable forge is right;
	// deferring forever is its own wrong durable answer — an outage, or a
	// credential revoked after the intent was created, would leave the run
	// alive past its deadline indefinitely. Measured from the run's wall
	// deadline, in SQL, against the database clock.
	publishSettleUnverifiableAfter = 30 * time.Minute
)

// inspectPublication answers reconciliation's question for a publish node:
// what does the durable record say, and does the forge agree?
//
// The Executor never saw this attempt — a publication is a control-plane
// effect — so its durable position IS the intent row. The four positions map
// cleanly:
//
//   - no intent      → nothing happened. Not completed, and NOT a loss: the
//     next attempt authorizes from scratch. Reported as an uncharged transient
//     so the node's failure budget is untouched, on publish_not_started's own
//     schedule ({0,0,0}, retry.go) so the requeue is immediate.
//   - halted         → terminal. The typed permanent conflict, so the run dies
//     naming what a human has to reconcile.
//   - pending/pushed → resume. Re-drive the state machine from the recorded
//     position; both steps are idempotent by probe.
//   - published      → done. Adopt the publication artifact.
//
// Returning Completed:false anywhere here would be a bug, not a shorthand:
// reconcileAttempt turns every incomplete inspection into a CHARGEABLE
// reconcile_lost, and a publication that loses its lease near max_attempts
// would exhaust and terminalize the run with a branch already on the forge and
// no later corrector.
func (o *Orchestrator) inspectPublication(ctx context.Context, ref AttemptRef) (InspectResult, error) {
	q := store.New(o.svc.pool)
	intent, err := q.GetPublishIntent(ctx, store.GetPublishIntentParams{
		RunID: ref.RunID, NodeID: ref.NodeID,
	})
	if errorsIsNoRows(err) {
		// Nothing was authorized, so nothing was published. Say so with an
		// uncharged transient rather than reconcile_lost: no work was lost, and
		// charging here would spend a retry on a worker that died before it did
		// anything at all.
		return InspectResult{Completed: true, Outcome: &Outcome{
			Class: OutcomeTransient, Code: CodePublishNotStarted,
			Message: "publication had not begun when its worker died",
		}}, nil
	}
	if err != nil {
		// A blip reading the row is not an answer about the publication, and it
		// must not be charged for one. Returning the error here would reach
		// reconcileAttempt's `if err != nil || !ins.Completed` and become a
		// CHARGEABLE reconcile_lost — the one spend this whole path exists to
		// forbid, through the only door that is a database round trip rather
		// than a forge call. With a warm plan cache this read is the FIRST round
		// trip a publish reconciliation makes, so a pool timeout lands here
		// before anything else; three across the node's life exhaust it and
		// terminalize the run with its branch already on the forge.
		//
		// Uncharged and resumable is also the honest answer: the durable
		// position is intact and unread, so asking again is the whole remedy.
		return InspectResult{Completed: true, Outcome: &Outcome{
			Class: OutcomeTransient, Code: CodePublishResumable,
			Message: fmt.Sprintf("could not read the publication's intent: %v", err),
		}}, nil
	}
	switch publishStatus(intent.Status) {
	case publishHalted:
		// Rendered by haltedOutcome, never here: the arriving-attempt answer
		// and the reconciled answer must be the same sentence.
		out := ClassifyOutcome(haltedOutcome(intent))
		return InspectResult{Completed: true, Outcome: &out}, nil
	case publishPublished:
		// Adopting is idempotent, and what makes it so is the finishPhase CAS
		// (reconciling -> succeeded matches exactly once), NOT a constraint:
		// artifacts carries plain indexes only. A refactor of evaluateSuccess
		// that loosened that CAS would silently start duplicating publication
		// artifacts here.
		return InspectResult{Completed: true, Result: ExecutionResult{
			Artifacts: []NewArtifact{publicationArtifact(intent)},
		}}, nil
	}
	// pending or pushed: the publication is real and unfinished, and the next
	// attempt resumes from this exact row. Nothing was lost.
	return InspectResult{Completed: true, Outcome: &Outcome{
		Class: OutcomeTransient, Code: CodePublishResumable,
		Message: fmt.Sprintf("publication is %s and resumes from its intent", intent.Status),
	}}, nil
}

// settleRunPublications gives every unsettled publication of an expired run a
// final, durable answer, and reports whether the run may now be terminalized.
//
// The asymmetry with reconciliation is deliberate. Reconciliation RESUMES a
// publication: it may push, it may open a pull request. This may do neither —
// the run is past its deadline and has no authority to cause anything. It only
// ASKS the forge what already exists and writes down the answer:
//
//   - the marker is absent → NOT an answer yet, until absence is STABLE (see
//     recordStableAbsence). Once it is, the intent is halted with a reason so
//     the row stops reading as in-flight.
//   - the marker is ours   → record the receipt the dying attempt never got to.
//     The effect is real, so the record must say so even though the run dies.
//   - the marker exists and we cannot prove it is ours → halt with the
//     conflict, which is the same answer the live path gives.
//
// "Ours" is the hard part, because the case that matters most is the one with
// NO receipt: the push landed and head_sha is still NULL, so there is nothing
// stored to compare the remote head against. The forge port cannot answer it
// (it reads a branch sha and no more) and the publisher's PushPatch must not be
// asked (it is effectful). What closes it is GitPublisher.ExpectedHead: the
// same clone-apply-commit the publisher performs, stopping before the push,
// which reproduces the exact sha this authorization produces because every
// commit input is replayed from the intent row.
//
// A forge that cannot be reached returns false: the run stays matched by the
// expired-run predicate and is retried next tick, up to
// publishSettleUnverifiableAfter. Guessing here would make the wrong answer
// durable, and this is the one place with no later corrector.
func (o *Orchestrator) settleRunPublications(ctx context.Context, runID uuid.UUID) bool {
	q := store.New(o.svc.pool)
	intents, err := q.ListUnsettledPublishIntents(ctx, runID)
	if err != nil {
		o.log.Error("orchestrator: unsettled publications", "run", runID, "error", err)
		return false
	}
	if len(intents) == 0 {
		return true
	}
	// Quiescence, once for the run: a phase still 'running' may be inside a
	// forge call right now, and probing under it would race the effect.
	//
	// It is spelled "nothing still running" and not "no live phase row", which
	// is the obvious phrasing and deadlocks the sweep: fencing leaves every
	// phase 'reconciling', which is NOT terminal, so a live-row test would
	// never pass on exactly the runs this exists for. What quiescence has to
	// mean is FENCED — the worker has lost the CAS it would need to commit
	// anything, so no new effect can be issued.
	phases, err := q.ListPhasesForRun(ctx, runID)
	if err != nil {
		o.log.Error("orchestrator: settle publications phases", "run", runID, "error", err)
		return false
	}
	for _, p := range phases {
		if PhaseStatus(p.Status) == PhaseRunning {
			return false // not fenced yet; settleExpiredPhases gets it next tick
		}
	}

	settled := true
	for _, intent := range intents {
		ok, err := o.settlePublication(ctx, q, intent)
		if err == nil {
			if !ok {
				settled = false
			}
			continue
		}
		// Never guess. A forge we cannot reach leaves the run matched by the
		// expired-run predicate and the next tick asks again — but only up to
		// the bound, or a long outage would make the run immortal.
		halted, herr := o.haltUnverifiablePublication(ctx, intent, err)
		if herr != nil {
			o.log.Error("orchestrator: halt unverifiable publication", "intent", intent.ID, "error", herr)
		}
		if halted {
			continue
		}
		o.log.Error("orchestrator: settle publication", "intent", intent.ID, "error", err)
		settled = false
	}
	return settled
}

// settlePublication gives ONE intent its final answer, or reports that it does
// not have one yet. It probes the marker its status makes uncertain, and it
// never causes anything.
func (o *Orchestrator) settlePublication(ctx context.Context, q *store.Queries,
	intent store.PublishIntent,
) (bool, error) {
	switch publishStatus(intent.Status) {
	case publishPending:
		return o.settleUnpushedPublication(ctx, q, intent)
	case publishPushed:
		return o.settleUnopenedPublication(ctx, q, intent)
	}
	return true, nil // 'published' and 'halted' are settled by definition
}

// settleUnpushedPublication answers "did the push land?" for an intent with no
// push receipt. The marker at risk is the BRANCH.
func (o *Orchestrator) settleUnpushedPublication(ctx context.Context, q *store.Queries,
	intent store.PublishIntent,
) (bool, error) {
	head, exists, err := o.svc.forge.BranchHead(ctx, intentRepo(intent), intent.Branch)
	if err != nil {
		return false, err
	}
	if !exists {
		return o.recordStableAbsence(ctx, q, intent,
			"expired before publishing: no branch on the forge after the settle grace")
	}
	if _, err := q.ClearPublicationAbsent(ctx, intent.ID); err != nil {
		return false, err
	}
	// A branch exists and there is no receipt to compare it against, so
	// ownership is proved the only way it can be: REBUILD the commit this
	// authorization produces and check the remote head against it (decision 18
	// — an observed sha is evidence, never permission).
	req, err := o.pushRequestFor(ctx, intent)
	if err != nil {
		return false, err
	}
	// The ctx is the real bound: PushRequest.Deadline is consulted by no
	// adapter today, and settlement runs on the janitor loop's context, which
	// has no deadline — so a clone against a wedged forge would block the whole
	// janitor goroutine, deadline sweep and lease sweep with it.
	probeCtx, cancel := context.WithTimeout(ctx, publishSettleProbeBudget)
	defer cancel()
	want, err := o.svc.publisher.ExpectedHead(probeCtx, req)
	if err != nil {
		return false, err
	}
	if want != head {
		return o.halt(ctx, intent, fmt.Sprintf(
			"branch %s exists at %s, which this authorization did not produce", intent.Branch, head))
	}
	// Ours, and it landed. Write the receipt the dying attempt never got to.
	n, err := q.RecordPushReceipt(ctx, store.RecordPushReceiptParams{
		ID: intent.ID, HeadSha: pgtype.Text{String: head, Valid: true}, UpdatedAt: o.svc.clock.Now(),
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		// A live driver beat us to it. Only a settled row costs this run
		// nothing: anything else is re-read next tick.
		return o.intentSettledElsewhere(ctx, q, intent)
	}
	// The intent is now 'pushed' and its pull request is the next question.
	// Deferring one tick keeps this function to one effect-shaped answer at a
	// time, and the run is already staying alive for the credential sweep.
	return false, nil
}

// settleUnopenedPublication answers "did the pull request land?" for an intent
// whose push receipt is recorded. Its window is identical to the branch's: a
// create call issued just before the deadline can still be on the wire.
func (o *Orchestrator) settleUnopenedPublication(ctx context.Context, q *store.Queries,
	intent store.PublishIntent,
) (bool, error) {
	candidates, err := o.svc.forge.FindPullRequests(ctx, intentRepo(intent), intent.Branch)
	if err != nil {
		return false, err
	}
	// ambiguous is deliberately discarded rather than tested beside the count:
	// every ambiguous answer matchPublication gives is reached from inside its
	// candidate loop, so it already implies len(candidates) > 0. Naming it in
	// the halt condition would read as a case that can occur on its own.
	pr, found, _ := matchPublication(candidates, intent)
	switch {
	case found:
		if _, err := q.ClearPublicationAbsent(ctx, intent.ID); err != nil {
			return false, err
		}
		n, err := q.RecordPRReceipt(ctx, store.RecordPRReceiptParams{
			ID:        intent.ID,
			PrNumber:  pgtype.Int8{Int64: pr.Number, Valid: true},
			PrUrl:     pgtype.Text{String: pr.URL, Valid: true},
			UpdatedAt: o.svc.clock.Now(),
		})
		if err != nil {
			return false, err
		}
		if n == 0 {
			return o.intentSettledElsewhere(ctx, q, intent)
		}
		return true, nil
	case len(candidates) > 0:
		if _, err := q.ClearPublicationAbsent(ctx, intent.ID); err != nil {
			return false, err
		}
		return o.halt(ctx, intent, fmt.Sprintf(
			"branch %s carries %d pull request(s) and none is provably this publication's",
			intent.Branch, len(candidates)))
	default:
		// No pull request visible. This is the window a branch-only stable
		// negative misses entirely: the create call may be on the wire right
		// now, so absence gets the same grace the branch does.
		return o.recordStableAbsence(ctx, q, intent,
			"expired after pushing: no pull request on the forge after the settle grace")
	}
}

// recordStableAbsence is the whole reason one probe never settles a negative.
//
// A cancelled request cannot prove the remote refused it, and that cuts both
// ways: a push (or a create) issued microseconds before the deadline may still
// be in flight when settlement probes. Halting on sight would terminalize the
// run, and then the effect lands — an orphan on the forge that no path
// examines, because the run is dead and its phases superseded. Fencing does not
// close it either: fencing changes database state, while the request is already
// on the wire.
//
// So: stamp the first sighting and defer; halt only once the grace has elapsed
// with the marker continuously missing. Any probe that finds the marker clears
// the stamp, and so does either receipt.
func (o *Orchestrator) recordStableAbsence(ctx context.Context, q *store.Queries,
	intent store.PublishIntent, reason string,
) (bool, error) {
	halted, err := o.settlementHalt(ctx, intent, reason, nil,
		func(ctx context.Context, tq *store.Queries) (int64, error) {
			return tq.HaltStablyAbsentPublication(ctx, store.HaltStablyAbsentPublicationParams{
				ID: intent.ID, HaltReason: pgtype.Text{String: reason, Valid: true},
				Status: intent.Status, GraceSeconds: int32(publishSettleGrace / time.Second),
			})
		})
	if err != nil {
		return false, err
	}
	if halted {
		return true, nil // absence held for the whole grace; the intent is settled
	}
	// Either this is the first sighting, or the grace has not elapsed. Stamp it
	// if unstamped (the statement is a no-op otherwise) and defer.
	if _, err := q.MarkPublicationAbsent(ctx, store.MarkPublicationAbsentParams{
		ID: intent.ID, Status: intent.Status,
	}); err != nil {
		return false, err
	}
	return false, nil
}

// haltUnverifiablePublication is the bound on "ask again next tick". It fires
// only when the run has been past its wall deadline for
// publishSettleUnverifiableAfter and settlement still cannot get an answer.
//
// The halt reason names the forge because that is the failure this bound
// exists for; the cause that actually stopped this pass rides in the event
// payload, so an operator reading the run's stream sees the real error.
func (o *Orchestrator) haltUnverifiablePublication(ctx context.Context,
	intent store.PublishIntent, cause error,
) (bool, error) {
	reason := fmt.Sprintf(
		"expired and unverifiable: the forge could not be reached for %s after the run's deadline",
		publishSettleUnverifiableAfter)
	return o.settlementHalt(ctx, intent, reason, map[string]any{"cause": cause.Error()},
		func(ctx context.Context, tq *store.Queries) (int64, error) {
			return tq.HaltUnverifiablePublication(ctx, store.HaltUnverifiablePublicationParams{
				ID: intent.ID, HaltReason: pgtype.Text{String: reason, Valid: true},
				Status:              intent.Status,
				UnverifiableSeconds: int32(publishSettleUnverifiableAfter / time.Second),
			})
		})
}

// halt is the settlement-side halt: the same row write haltPublication makes,
// without the attempt-scoped correlation a dead run has no `claimed` to supply.
//
// A lost CAS means a live driver moved the intent. Only a SETTLED row lets this
// run terminalize now; anything else is re-read next tick.
func (o *Orchestrator) halt(ctx context.Context, intent store.PublishIntent, reason string) (bool, error) {
	halted, err := o.settlementHalt(ctx, intent, reason, nil,
		func(ctx context.Context, tq *store.Queries) (int64, error) {
			return tq.HaltPublishIntent(ctx, store.HaltPublishIntentParams{
				ID: intent.ID, HaltReason: pgtype.Text{String: reason, Valid: true},
				UpdatedAt: o.svc.clock.Now(),
			})
		})
	if err != nil {
		return false, err
	}
	if halted {
		return true, nil
	}
	return o.intentSettledElsewhere(ctx, store.New(o.svc.pool), intent)
}

// settlementHalt runs one halting statement in a transaction with the event and
// the audit entry EVERY halt in this system carries.
//
// One emission path for all three settlement halts, because the most common one
// is the timed-out absence: writing that through its CAS alone would flip an
// intent to 'halted' with no publish.halted event and no audit row, and an
// operator reading the run's stream would see a run that died over a
// publication that silently changed status.
//
// The correlation id is the run's, matching terminalizeRun: a dead run has no
// attempt to name. The audit entry needs no actor, exactly as haltPublication's
// does not.
func (o *Orchestrator) settlementHalt(ctx context.Context, intent store.PublishIntent,
	reason string, extra map[string]any,
	halt func(ctx context.Context, q *store.Queries) (int64, error),
) (bool, error) {
	// GetRunProjectID rather than loadPlan: it cannot fail for compile reasons,
	// and a dead run's definition is not worth recompiling to name a project.
	projectID, err := store.New(o.svc.pool).GetRunProjectID(ctx, intent.RunID)
	if err != nil {
		return false, err
	}
	now := o.svc.clock.Now()
	var n int64
	err = pg.WithTx(ctx, o.svc.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var herr error
		if n, herr = halt(ctx, q); herr != nil {
			return herr
		}
		if n == 0 {
			return nil // the CAS matched nothing; the caller decides what that means
		}
		payload := map[string]any{"node_id": intent.NodeID, "branch": intent.Branch, "reason": reason}
		for k, v := range extra {
			payload[k] = v
		}
		if err := appendEvents(ctx, tx, now, []Event{{
			RunID: intent.RunID, ProjectID: projectID, Kind: EventPublishHalted,
			CorrelationID: "caesar-run-" + intent.RunID.String(), Payload: payload,
		}}); err != nil {
			return err
		}
		return o.svc.audit.Record(ctx, tx, audit.Entry{
			Action: "publish.halted", SubjectKind: "publish_intent", SubjectID: intent.ID.String(),
			Detail: map[string]any{
				"run_id": intent.RunID.String(), "node_id": intent.NodeID,
				"branch": intent.Branch, "reason": reason,
			},
		})
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// intentSettledElsewhere re-reads an intent whose CAS matched nothing and
// reports whether somebody else already gave it a final answer. A settled row
// costs this run no further tick; anything else does.
func (o *Orchestrator) intentSettledElsewhere(ctx context.Context, q *store.Queries,
	intent store.PublishIntent,
) (bool, error) {
	row, err := q.GetPublishIntent(ctx, store.GetPublishIntentParams{
		RunID: intent.RunID, NodeID: intent.NodeID,
	})
	if err != nil {
		return false, err
	}
	switch publishStatus(row.Status) {
	case publishPublished, publishHalted:
		return true, nil
	}
	return false, nil
}

// pushRequestFor rebuilds the PushRequest this intent's authorization implies,
// entirely from durable rows — settlement holds no `claimed` and no live plan.
//
// ExpectedHead's answer is only worth anything if every input that reaches the
// commit is identical to what PushPatch used, so each field is sourced from the
// same row the live path read it from: the message and the author address were
// rendered once inside the authorize transaction and stored, and the date is
// the intent's created_at. Re-resolving any of them would rebuild a DIFFERENT
// commit and turn a crash-after-push into a permanent publish_conflict on a
// branch that is genuinely ours.
//
// ExpectHead is deliberately absent: ExpectedPublicationHead calls only
// publishPrepare and returns the rebuilt head, and in.ExpectHead is consulted
// only inside Publish, where the precondition guards a write.
func (o *Orchestrator) pushRequestFor(ctx context.Context, intent store.PublishIntent) (PushRequest, error) {
	q := store.New(o.svc.pool)
	row, err := q.GetAuthorizationByID(ctx, intent.AuthorizationID)
	if err != nil {
		return PushRequest{}, err
	}
	art, err := q.GetArtifactByID(ctx, row.ArtifactID)
	if err != nil {
		return PushRequest{}, err
	}
	diff, ok := artifactDiff(art)
	if !ok {
		return PushRequest{}, fmt.Errorf("runs: approved artifact %s carries no diff", row.ArtifactID)
	}
	return PushRequest{
		Ref:      AttemptRef{RunID: intent.RunID, NodeID: intent.NodeID, Attempt: intent.Attempt},
		Repo:     intentRepo(intent),
		Branch:   intent.Branch,
		BaseSHA:  intent.BaseSha,
		TreeHash: intent.TreeHash,
		Patch:    diff,
		Message:  intent.CommitMessage,
		// Replayed, never re-resolved: publish.go's live push does the same,
		// and the sha hashes both.
		AuthorName: emailLocalPart(intent.AuthorEmail), AuthorEmail: intent.AuthorEmail,
		Date: intent.CreatedAt,
		// Set for parity with the live path only. No adapter reads this field;
		// the probeCtx at the call site is the real bound. It is NOT the run's
		// wall deadline, which is by definition already in the past here —
		// passing that would abort the rebuild before it started and turn "I
		// could not check" into a permanent-looking failure.
		Deadline: o.svc.clock.Now().Add(publishSettleProbeBudget),
	}, nil
}

func publicationArtifact(intent store.PublishIntent) NewArtifact {
	payload := map[string]any{
		"branch": intent.Branch, "target_branch": intent.TargetBranch,
		"base_sha": intent.BaseSha, "tree_hash": intent.TreeHash,
		"head_sha": derefText(intent.HeadSha),
		"pr_url":   derefText(intent.PrUrl),
	}
	if intent.PrNumber.Valid {
		payload["pr_number"] = intent.PrNumber.Int64
	}
	return NewArtifact{Type: ArtifactKindPublication, Payload: payload}
}
