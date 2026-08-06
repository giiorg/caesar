// In-package: buildRequest is the orchestrator's own assembly step and has no
// exported surface. What it must get right is the difference between a harness
// that is GONE and a database that is briefly unreachable, and that difference
// is only visible from inside.
package runs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

// stubHarnessSource answers the one port call buildRequest makes.
type stubHarnessSource struct {
	ref HarnessRef
	err error
}

func (s stubHarnessSource) HarnessByName(context.Context, string) (HarnessRef, error) {
	return s.ref, s.err
}

// Resolving a harness is two round trips now — the harness row, then the
// credential row whose kind the attempt is pinned to (app/runsports.go) — so
// the odds of meeting a pool timeout here doubled. Reporting one as "no longer
// registered" is permanent, kills the node with no retry, and tells the
// operator to re-register something that never went anywhere. Only
// ClassNotFound is that fact; everything else waits.
func TestBuildRequest_HarnessResolutionSeparatesGoneFromUnreachable(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantClass OutcomeClass
		wantCode  string
		wantMsg   string
	}{
		{
			name:      "the harness is gone",
			err:       xerrors.E(xerrors.ClassNotFound, "no such harness"),
			wantClass: OutcomePermanent, wantCode: CodeHarnessError,
			wantMsg: "no longer registered",
		},
		{
			// What a pool timeout on either round trip actually looks like:
			// an unclassified wrapped error out of the assets aggregate.
			name:      "the database is unreachable",
			err:       errors.New("assets: get credential: timeout: context deadline exceeded"),
			wantClass: OutcomeTransient, wantCode: CodeInternalError,
			wantMsg: "resolve harness",
		},
		{
			// A classified non-NotFound error is still not a missing harness.
			name:      "the aggregate reports something else entirely",
			err:       xerrors.E(xerrors.ClassInternal, "decrypt credential kind"),
			wantClass: OutcomeTransient, wantCode: CodeInternalError,
			wantMsg: "resolve harness",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := &Orchestrator{svc: &Service{harnesses: stubHarnessSource{err: tc.err}}}
			ref := AttemptRef{RunID: uuid.New(), NodeID: "impl", Attempt: 1}

			_, err := o.buildRequest(context.Background(), cachedPlan{},
				PlanNode{ID: "impl", Kind: KindImplement, Harness: "claude"}, ref, time.Now().Add(time.Hour))

			var oe OutcomeError
			if !errors.As(err, &oe) {
				t.Fatalf("err = %v, want a typed OutcomeError", err)
			}
			if oe.Outcome.Class != tc.wantClass || oe.Outcome.Code != tc.wantCode {
				t.Errorf("outcome = %s/%s, want %s/%s",
					oe.Outcome.Class, oe.Outcome.Code, tc.wantClass, tc.wantCode)
			}
			if !strings.Contains(oe.Outcome.Message, tc.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", oe.Outcome.Message, tc.wantMsg)
			}
			// A wait that charges the node is still a wait, but a transient
			// internal error must not claim the harness vanished: that is the
			// sentence an operator acts on.
			if tc.wantClass == OutcomeTransient && strings.Contains(oe.Outcome.Message, "no longer registered") {
				t.Errorf("message = %q, want it not to assert the harness was deregistered", oe.Outcome.Message)
			}
		})
	}
}

// The successful resolution pins BOTH halves of the binding into the request:
// the credential id and the kind, which is what lets the runner derive custody
// without ever re-reading the mutable harness row.
func TestBuildRequest_PinsTheResolvedCredentialBinding(t *testing.T) {
	harnessID, credID := uuid.New(), uuid.New()
	o := &Orchestrator{svc: &Service{harnesses: stubHarnessSource{ref: HarnessRef{
		ID: harnessID, Kind: "codex", CredentialID: credID, CredentialKind: "agent_auth_file",
	}}}}
	ref := AttemptRef{RunID: uuid.New(), NodeID: "impl", Attempt: 1}

	req, err := o.buildRequest(context.Background(), cachedPlan{},
		PlanNode{ID: "impl", Kind: KindImplement, Harness: "codex-main"}, ref, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if req.HarnessID != harnessID {
		t.Errorf("harness id = %s, want %s", req.HarnessID, harnessID)
	}
	want := CredentialBinding{ID: credID, Kind: "agent_auth_file"}
	if req.Credential != want {
		t.Errorf("binding = %+v, want %+v", req.Credential, want)
	}
}
