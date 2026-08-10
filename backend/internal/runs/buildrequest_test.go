// In-package: buildRequest is the orchestrator's own assembly step and has no
// exported surface. What it must get right is the difference between a harness
// that is GONE and a database that is briefly unreachable, and which of two
// patch inputs a revised attempt actually runs — both only visible from
// inside, and both reachable without a database.
package runs

import (
	"context"
	"errors"
	"strings"
	"testing"

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
func TestHarnessBinding_SeparatesGoneFromUnreachable(t *testing.T) {
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

			_, _, err := o.harnessBinding(context.Background(),
				PlanNode{ID: "impl", Kind: KindImplement, Harness: "claude"})

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

// The successful resolution reports BOTH halves of the binding — the
// credential id and the kind — which is what lets the runner derive custody
// without ever re-reading the mutable harness row. That the two then reach the
// request is pinned end to end by
// TestOrchestrator_RequestPinsTheHarnessCredential.
func TestHarnessBinding_ReportsBothHalvesOfTheResolvedCredential(t *testing.T) {
	harnessID, credID := uuid.New(), uuid.New()
	o := &Orchestrator{svc: &Service{harnesses: stubHarnessSource{ref: HarnessRef{
		ID: harnessID, Kind: "codex", CredentialID: credID, CredentialKind: "agent_auth_file",
	}}}}

	gotID, gotBinding, err := o.harnessBinding(context.Background(),
		PlanNode{ID: "impl", Kind: KindImplement, Harness: "codex-main"})
	if err != nil {
		t.Fatalf("harnessBinding: %v", err)
	}
	if gotID != harnessID {
		t.Errorf("harness id = %s, want %s", gotID, harnessID)
	}
	want := CredentialBinding{ID: credID, Kind: "agent_auth_file"}
	if gotBinding != want {
		t.Errorf("binding = %+v, want %+v", gotBinding, want)
	}
}

// A node that declares no harness resolves nothing and reports no error: gates
// and git-publish nodes reach buildRequest too, and neither runs an agent.
func TestHarnessBinding_ANodeWithNoHarnessBindsNothing(t *testing.T) {
	o := &Orchestrator{svc: &Service{harnesses: stubHarnessSource{
		err: xerrors.E(xerrors.ClassNotFound, "no such harness"),
	}}}
	gotID, gotBinding, err := o.harnessBinding(context.Background(),
		PlanNode{ID: "publish", Kind: KindGitPublish})
	if err != nil {
		t.Fatalf("a harnessless node must not consult the registry at all: %v", err)
	}
	if gotID != uuid.Nil || gotBinding != (CredentialBinding{}) {
		t.Errorf("bound %s / %+v, want nothing", gotID, gotBinding)
	}
}

// One patch reaches a phase, ever (Plan 07's cumulative-patch contract): the
// runner refuses a second one outright (runner/execute.go lineageInto), so
// buildRequest has to resolve the collision rather than pass it on. A revision
// seeds the target with the patch the gate examined, and that seed is strictly
// newer than whatever an ancestor outside the slice still holds.
func TestCollapsePatchInputs(t *testing.T) {
	note := ArtifactInput{ID: uuid.New(), Type: ArtifactKindGateNote}
	plan := ArtifactInput{ID: uuid.New(), Type: ArtifactKindPlan}
	structural := ArtifactInput{ID: uuid.New(), Type: ArtifactKindPatch}
	seed := ArtifactInput{ID: uuid.New(), Type: ArtifactKindPatch}

	cases := []struct {
		name string
		in   []ArtifactInput
		want []ArtifactInput
	}{
		{
			name: "no patch at all passes through untouched",
			in:   []ArtifactInput{plan, note},
			want: []ArtifactInput{plan, note},
		},
		{
			name: "the single structural patch survives",
			in:   []ArtifactInput{plan, structural},
			want: []ArtifactInput{plan, structural},
		},
		{
			// The order buildRequest produces: structural inputs, then the
			// revision's seeds. The seed is last and the seed is what runs.
			name: "a seeded patch displaces the structural one",
			in:   []ArtifactInput{plan, structural, note, seed},
			want: []ArtifactInput{plan, note, seed},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := collapsePatchInputs(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d inputs, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].ID != tc.want[i].ID {
					t.Errorf("input %d = %s (%s), want %s (%s)",
						i, got[i].ID, got[i].Type, tc.want[i].ID, tc.want[i].Type)
				}
			}
		})
	}
}
