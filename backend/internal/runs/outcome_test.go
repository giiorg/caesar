package runs_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

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

func TestOutcomeRetryable(t *testing.T) {
	if !(runs.Outcome{Class: runs.OutcomeTransient}).Retryable() {
		t.Error("transient must be retryable")
	}
	if (runs.Outcome{Class: runs.OutcomePermanent}).Retryable() || (runs.Outcome{Class: runs.OutcomePolicyViolation}).Retryable() {
		t.Error("permanent and policy_violation must not be retryable")
	}
}
