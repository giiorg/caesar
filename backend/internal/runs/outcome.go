package runs

import (
	"context"
	"errors"
	"time"
)

// OutcomeClass is the typed outcome taxonomy (spec §2): transient
// retries until attempts exhaust; everything else is final. Unclassified
// errors are permanent — default-deny.
type OutcomeClass string

const (
	OutcomeTransient       OutcomeClass = "transient"
	OutcomePermanent       OutcomeClass = "permanent"
	OutcomePolicyViolation OutcomeClass = "policy_violation"
)

// Pinned outcome codes (plan header). Attempt-level codes come from
// classification; run-level codes are stamped by the orchestrator.
const (
	CodeHarnessError         = "harness_error"
	CodeTimeout              = "timeout"
	CodeReconcileLost        = "reconcile_lost"
	CodeNotImplemented       = "not_implemented"
	CodeInternalError        = "internal_error"
	CodeInvalidOutput        = "invalid_output"
	CodeAttemptsExhausted    = "attempts_exhausted"
	CodeBudgetExhausted      = "budget_exhausted"
	CodeWallDeadlineExceeded = "wall_deadline_exceeded"
	CodeFenced               = "fenced"
	CodeSuperseded           = "superseded"

	// Plan 06 runner codes.
	CodeImageUnavailable          = "image_unavailable"
	CodeStagingFailed             = "staging_failed"
	CodeStagingVerificationFailed = "staging_verification_failed"
	CodeBaseSHAUnavailable        = "base_sha_unavailable"
	CodeWorkspaceQuotaExceeded    = "workspace_quota_exceeded"
	CodeLogFlood                  = "log_flood"
	CodeExtractionFailed          = "extraction_failed"
	CodePatchTooLarge             = "patch_too_large"
	CodeEmbeddedRepo              = "embedded_repo"

	// Plan 07: subscription-auth outcomes (spec 2026-08-05 §2, §4).
	CodeRateLimited           = "rate_limited"           // cap window exhausted — waiting, not failing
	CodeAuthExpired           = "auth_expired"           // credential dead; message names it
	CodeCredentialBusy        = "credential_busy"        //nolint:gosec // G101 matches the name: this is a state, not a secret — single-flight lease held elsewhere; charges nothing
	CodeCredentialQuarantined = "credential_quarantined" //nolint:gosec // G101 matches the name: this is a state, not a secret — read-back identity mismatch (§2.5)
	CodeSecretInArtifact      = "secret_in_artifact"     // redaction scan hit; artifact withheld (§3)

	// Plan 08: gate and publish outcomes (spec §2 Gate, §3 Publishing).
	CodeGateRejected            = "gate_rejected"             // a human ended the run at a gate
	CodeRevisionRoundsExhausted = "revision_rounds_exhausted" // max_rounds spent; only approve/reject remain
	CodeAuthorizationStale      = "authorization_stale"       // the ApprovedPatch no longer matches what it authorized
	CodePublishConflict         = "publish_conflict"          // a marker exists in a state we cannot prove is ours
	CodePushRejected            = "push_rejected"             // the expected-remote-SHA precondition failed
	CodeForgeUnavailable        = "forge_unavailable"         // the forge API could not be reached or refused transiently
	CodePublishNotStarted       = "publish_not_started"       // transient, UNCHARGED: the worker died before authorizing
	CodePublishResumable        = "publish_resumable"         // transient, UNCHARGED: an unfinished publication resumes from its intent
	// CodeRepoUnavailable is the forge refusing to show the repository at all
	// — deleted, renamed, or no longer visible to this token. Permanent, and
	// one of the answers a publication HALTS on rather than retries: no number
	// of attempts brings a deleted repository back, and a run that merely
	// failed would leave its pushed branch with nobody looking for it.
	CodeRepoUnavailable = "repo_unavailable"
)

// Outcome is a typed terminal result for an attempt or a run. RetryAt is
// the moment a waiting outcome (a cap window, a held lease) becomes worth
// retrying; nothing else sets it.
type Outcome struct {
	Class   OutcomeClass `json:"class"`
	Code    string       `json:"code"`
	Message string       `json:"message"`
	RetryAt *time.Time   `json:"retry_at,omitempty"`
}

func (o Outcome) Retryable() bool { return o.Class == OutcomeTransient }

// Chargeable reports whether this failure spends the phase's failure budget.
// Cap waits, lease contention, a publication that never began and one that is
// merely unfinished are facts of life rather than errors: they requeue without
// charging (spec §2.2, §4.3). The two publish codes matter most — charging
// them would let a lease hiccup or a forge outage exhaust a node whose branch
// is already on the forge, terminalizing the run over work that succeeded.
func (o Outcome) Chargeable() bool {
	if o.Class != OutcomeTransient {
		return false
	}
	switch o.Code {
	case CodeRateLimited, CodeCredentialBusy, CodePublishNotStarted, CodePublishResumable:
		return false
	}
	return true
}

// exemptFromBudgetPreCheck names the outcomes evaluateFailure must not demote
// to a permanent budget_exhausted.
//
// Chargeable above is about the NODE's failure budget; this is about the RUN's
// execution budget, and they are separate bounds with separate doors into a
// terminalized run. An outcome that charges nothing has, by definition, not
// spent the time the pre-check is protecting, so demoting its requeue would
// kill a run for time it never spent — credential_busy's original reasoning,
// which applies unchanged to both publish codes.
//
// It matters most for those two: a publish attempt that fails here fails with
// an intent that is 'pending' or 'pushed', and a run terminalized 'failed' is
// revisited by nothing (ListExpiredRunIDs matches only pending, running and
// waiting), so the branch is an orphan. Exempt, the usage still accrues — so
// once it crosses the budget the successor stops satisfying ClaimPhase's
// predicate and the publication waits for the wall deadline, which IS swept.
func exemptFromBudgetPreCheck(code string) bool {
	switch code {
	case CodeCredentialBusy, CodePublishNotStarted, CodePublishResumable:
		return true
	}
	return false
}

// exemptFromParkPastDeadline names the outcomes evaluateFailure must park even
// when the wait would wake past the run's wall deadline.
//
// The refusal it exempts them from is stated as "never park a row that could
// not legally run when it woke" — but ClaimPhase already enforces exactly that
// (`AND r.wall_deadline > now()`), so the refusal is belt-and-braces, and for
// an unfinished publication the braces cost more than they hold up.
// publish_resumable backs off ~15 seconds, so ANY uncharged publish failure
// inside a backoff of the deadline would terminalize the run 'failed' with the
// intent still 'pending' or 'pushed' — and at a moment when wall_deadline is
// still in the FUTURE, so ListExpiredRunIDs (pending, running, waiting) never
// matches that run again and the branch is an orphan. Parked instead, the row
// is unclaimable, the run sits 'running' until the deadline passes, and the
// expiry sweep hands it to settlement, which finishes the record.
//
// This is deliberately NOT the exemptFromBudgetPreCheck set, and the two must
// not be merged: that predicate asks "did this outcome spend the time the
// budget protects?", which credential_busy answers no to as squarely as the
// publish codes do. This one asks "would terminalizing here strand an effect
// no sweep revisits?" — which only an unfinished publication answers yes to. A
// credential_busy attempt holds nothing of ours, so a run that runs out of wall
// clock waiting for a lease is honestly dead.
func exemptFromParkPastDeadline(code string) bool {
	switch code {
	case CodePublishNotStarted, CodePublishResumable:
		return true
	}
	return false
}

// Map renders the outcome for event payloads and jsonb columns.
func (o Outcome) Map() map[string]any {
	m := map[string]any{"class": string(o.Class), "code": o.Code, "message": o.Message}
	if o.RetryAt != nil {
		m["retry_at"] = o.RetryAt.Format(time.RFC3339)
	}
	return m
}

// OutcomeError carries a typed outcome across the Executor port boundary.
type OutcomeError struct{ Outcome Outcome }

func (e OutcomeError) Error() string { return e.Outcome.Code + ": " + e.Outcome.Message }

// ClassifyOutcome maps an executor error to a typed outcome: typed
// errors pass through, a context deadline is a transient timeout, and
// anything unclassified is permanent (spec §2 default-deny).
func ClassifyOutcome(err error) Outcome {
	var oe OutcomeError
	if errors.As(err, &oe) {
		return oe.Outcome
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Outcome{Class: OutcomeTransient, Code: CodeTimeout, Message: "attempt deadline exceeded"}
	}
	return Outcome{Class: OutcomePermanent, Code: CodeInternalError, Message: err.Error()}
}
