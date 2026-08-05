package runs_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/giiorg/caesar/backend/internal/runs"
)

func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want runs.Outcome
	}{
		{
			name: "typed outcome error passes through",
			err:  runs.OutcomeError{Outcome: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeHarnessError, Message: "boom"}},
			want: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeHarnessError, Message: "boom"},
		},
		{
			name: "wrapped outcome error passes through",
			err:  fmt.Errorf("execute: %w", runs.OutcomeError{Outcome: runs.Outcome{Class: runs.OutcomePermanent, Code: runs.CodeNotImplemented, Message: "no runner"}}),
			want: runs.Outcome{Class: runs.OutcomePermanent, Code: runs.CodeNotImplemented, Message: "no runner"},
		},
		{
			name: "context deadline is a transient timeout",
			err:  fmt.Errorf("execute: %w", context.DeadlineExceeded),
			want: runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeTimeout, Message: "attempt deadline exceeded"},
		},
		{
			name: "unclassified is permanent (default-deny)",
			err:  errors.New("some surprise"),
			want: runs.Outcome{Class: runs.OutcomePermanent, Code: runs.CodeInternalError, Message: "some surprise"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runs.ClassifyOutcome(tc.err); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestOutcome_ChargeableExemptsCapAndContentionWaits(t *testing.T) {
	cases := []struct {
		out  runs.Outcome
		want bool
	}{
		{runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeHarnessError}, true},
		{runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeRateLimited}, false},
		{runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeCredentialBusy}, false},
		{runs.Outcome{Class: runs.OutcomePermanent, Code: runs.CodeAuthExpired}, false}, // permanent: never retried, charge is moot
	}
	for _, c := range cases {
		if got := c.out.Chargeable(); got != c.want {
			t.Errorf("Chargeable(%s/%s) = %v, want %v", c.out.Class, c.out.Code, got, c.want)
		}
	}
}

func TestOutcome_MapCarriesRetryAt(t *testing.T) {
	at := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	m := (runs.Outcome{Class: runs.OutcomeTransient, Code: runs.CodeRateLimited, Message: "cap", RetryAt: &at}).Map()
	if m["retry_at"] != at.Format(time.RFC3339) {
		t.Fatalf("retry_at = %v", m["retry_at"])
	}
	if _, present := (runs.Outcome{Code: runs.CodeHarnessError}).Map()["retry_at"]; present {
		t.Fatal("retry_at must be absent when unset")
	}
}

func TestOutcomeRetryable(t *testing.T) {
	if !(runs.Outcome{Class: runs.OutcomeTransient}).Retryable() {
		t.Error("transient must be retryable")
	}
	if (runs.Outcome{Class: runs.OutcomePermanent}).Retryable() || (runs.Outcome{Class: runs.OutcomePolicyViolation}).Retryable() {
		t.Error("permanent and policy_violation must not be retryable")
	}
}
