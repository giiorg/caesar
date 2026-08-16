// In-package: pinnedFromVersion is the composition root's private mapping
// from the definitions vocabulary onto the runs mirror, and it is the only
// place both are visible at once.
package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

// gitPublisher's own pre-gitops failures are the one leak at this boundary.
//
// gitops.AsOutcomeError covers everything gitops returns, and nothing covered
// what happens BEFORE it is entered: resolving the clone URL, opening the
// credential (a vault or credential-service blip), and materializing the
// scratch directory. runs.ClassifyOutcome is default-deny, so returned raw
// each of those becomes a PERMANENT internal_error — and a permanent outcome
// out of the publish node terminalizes the run, which no sweep revisits.
//
// Nothing has happened when these fire, which is exactly what
// publish_not_started means.
func TestGitPublisher_FailuresBeforeGitopsAreUnchargedNonStarts(t *testing.T) {
	// scratch runs before prepare, so a workDir that cannot be written reaches
	// its return without a credential store standing behind it. Both legs are
	// therefore reachable from a plain unit test.
	unwritable := gitPublisher{workDir: filepath.Join(t.TempDir(), "does-not-exist")}
	badForge := gitPublisher{workDir: t.TempDir()}

	calls := map[string]func() error{
		"push cannot make its scratch directory": func() error {
			_, err := unwritable.PushPatch(context.Background(), runs.PushRequest{
				Repo: runs.RepoRef{Forge: "github", Slug: "a/b"},
			})
			return err
		},
		"push cannot resolve the forge": func() error {
			_, err := badForge.PushPatch(context.Background(), runs.PushRequest{
				Repo: runs.RepoRef{Forge: "bitbucket", Slug: "a/b"},
			})
			return err
		},
		"the rebuild cannot make its scratch directory": func() error {
			_, err := unwritable.ExpectedHead(context.Background(), runs.PushRequest{
				Repo: runs.RepoRef{Forge: "github", Slug: "a/b"},
			})
			return err
		},
		"the rebuild cannot resolve the forge": func() error {
			_, err := badForge.ExpectedHead(context.Background(), runs.PushRequest{
				Repo: runs.RepoRef{Forge: "bitbucket", Slug: "a/b"},
			})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("want an error; a stub that fakes success is worse than one that refuses")
			}
			out := runs.ClassifyOutcome(err)
			if out.Class != runs.OutcomeTransient || out.Code != runs.CodePublishNotStarted || out.Chargeable() {
				t.Fatalf("outcome = %+v; want a transient, uncharged publish_not_started", out)
			}
		})
	}
}

// A failure that already carries its own verdict keeps it: only untyped errors
// need one invented for them.
func TestNotStartedLeavesATypedOutcomeAlone(t *testing.T) {
	in := runs.OutcomeError{Outcome: runs.Outcome{
		Class: runs.OutcomePermanent, Code: runs.CodePublishConflict, Message: "the marker is not ours",
	}}
	out := runs.ClassifyOutcome(notStarted(in))
	if out.Class != runs.OutcomePermanent || out.Code != runs.CodePublishConflict {
		t.Fatalf("outcome = %+v; want the permanent conflict unchanged", out)
	}
	// OutcomeError carries no cause, so the wrap has to spell it out or the
	// reason a publication never began is lost between the vault and the run.
	wrapped := runs.ClassifyOutcome(notStarted(errors.New("vault: dial tcp: connection refused")))
	if !strings.Contains(wrapped.Message, "connection refused") {
		t.Errorf("message = %q; want the underlying cause named", wrapped.Message)
	}
}

func TestPinnedFromVersion_CarriesTheRevisionTransition(t *testing.T) {
	doc := workflows.Document{
		Schema: 1,
		Nodes: []workflows.Node{
			{ID: "implement", Type: workflows.NodeImplement, Harness: "h", Instructions: "x"},
			{ID: "gate", Type: workflows.NodeGate, OnRequestChanges: &workflows.RevisionTransition{Target: "implement", MaxRounds: 3}},
		},
		Edges: []workflows.Edge{{From: "implement", To: "gate", Artifact: workflows.ArtifactPatch}},
	}
	got, err := pinnedFromVersion(workflows.Version{ID: uuid.New(), Document: doc.Canonical()})
	if err != nil {
		t.Fatal(err)
	}
	var gate runs.DefNode
	for _, n := range got.Doc.Nodes {
		if n.ID == "gate" {
			gate = n
		}
	}
	if gate.Revision == nil {
		t.Fatal("the mirror must carry on_request_changes; without it no revision can ever open")
	}
	if gate.Revision.Target != "implement" || gate.Revision.MaxRounds != 3 {
		t.Errorf("revision = %+v; want implement/3", *gate.Revision)
	}
}

func TestPinnedFromVersion_LeavesNonGatesWithoutARevision(t *testing.T) {
	doc := workflows.Document{
		Schema: 1,
		Nodes:  []workflows.Node{{ID: "implement", Type: workflows.NodeImplement, Harness: "h", Instructions: "x"}},
	}
	got, err := pinnedFromVersion(workflows.Version{ID: uuid.New(), Document: doc.Canonical()})
	if err != nil {
		t.Fatal(err)
	}
	if got.Doc.Nodes[0].Revision != nil {
		t.Error("only gates carry a revision transition")
	}
}

// The line the payload-contract compatibility rule rests on. Without it every
// run looks like schema 0 to runs.ValidateArtifactPayload, nothing is ever
// checked, and the feature is silently absent — the failure mode that leaves
// no trace anywhere.
//
// Both versions are asserted with literals rather than workflows.SchemaVersion:
// a schema-1 document must yield 1, not "whatever this binary now writes".
func TestPinnedFromVersion_CarriesTheDocumentSchemaVersion(t *testing.T) {
	for _, schema := range []int{1, 2} {
		doc := workflows.Document{
			Schema: schema,
			Nodes:  []workflows.Node{{ID: "impl", Type: workflows.NodeImplement, Harness: "h", Instructions: "x"}},
		}
		got, err := pinnedFromVersion(workflows.Version{ID: uuid.New(), Document: doc.Canonical()})
		if err != nil {
			t.Fatal(err)
		}
		if got.Doc.Schema != schema {
			t.Errorf("Doc.Schema = %d, want %d", got.Doc.Schema, schema)
		}
	}
}

// The seam the payload contract rests on, end to end and in the one package
// that can see both vocabularies: a document's schema version survives
// parsing, the mirror, and compilation, and is still the number the payload
// validator is handed.
//
// It also ties two constants that live in contexts which cannot import each
// other — workflows.SchemaVersion, the version a new document is written as,
// and runs' internal threshold at which payload validation switches on. A
// binary where those disagree validates nothing while every test in both
// packages stays green.
func TestPinnedSchemaVersionIsWhatDecidesPayloadValidation(t *testing.T) {
	// A plan payload in the pre-contract shape: exactly what the fake executor
	// and every existing harness write today.
	legacy := map[string]any{"content": "plan: do the thing"}

	for _, c := range []struct {
		schema     int
		wantReject bool
	}{
		{schema: 1, wantReject: false},
		{schema: workflows.SchemaVersion, wantReject: true},
	} {
		doc := workflows.Document{
			Schema: c.schema,
			Nodes:  []workflows.Node{{ID: "plan", Type: workflows.NodePlan, Harness: "h", Instructions: "x"}},
		}
		pinned, err := pinnedFromVersion(workflows.Version{ID: uuid.New(), Document: doc.Canonical()})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := runs.Compile(pinned.Doc)
		if err != nil {
			t.Fatal(err)
		}
		err = runs.ValidateArtifactPayload(plan.DocSchemaVersion, runs.ArtifactKindPlan, legacy)
		if c.wantReject && err == nil {
			t.Errorf("schema %d accepted a pre-contract plan payload; the contract is switched off", c.schema)
		}
		if !c.wantReject && err != nil {
			t.Errorf("schema %d rejected a pre-contract plan payload: %v; a pinned v1 run must run exactly as it did", c.schema, err)
		}
	}
}
