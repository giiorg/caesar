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
// Cap waits and lease contention are facts of subscription life, not errors:
// they requeue without charging (spec §2.2, §4.3).
func (o Outcome) Chargeable() bool {
	return o.Class == OutcomeTransient && o.Code != CodeRateLimited && o.Code != CodeCredentialBusy
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
