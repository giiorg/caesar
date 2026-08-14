//go:build integration

package runs_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/audit"
	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/clock"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/runs"
	"github.com/giiorg/caesar/backend/internal/runs/fake"
)

// fixturePorts serves the fixture's own rows through the runs ports —
// integration tests below the app layer never import peer contexts.
type fixturePorts struct {
	f           fixture
	doc         runs.DefinitionDoc
	hash        string
	harness     uuid.UUID
	harnessCred uuid.UUID
	// The two doubles a publication drives. They are handed back with the
	// service so a test can inject into the very instances it wired.
	forge     *fake.Forge
	publisher *fake.Publisher
}

func (p fixturePorts) Definition(_ context.Context, workflowID uuid.UUID, version int32) (runs.PinnedDefinition, error) {
	if version != 1 {
		return runs.PinnedDefinition{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	return runs.PinnedDefinition{
		VersionID: p.f.versionID, WorkflowID: workflowID, Version: 1,
		ContentHash: p.hash, Doc: p.doc,
	}, nil
}

func (p fixturePorts) DefinitionByVersionID(_ context.Context, versionID uuid.UUID) (runs.PinnedDefinition, error) {
	if versionID != p.f.versionID {
		return runs.PinnedDefinition{}, xerrors.E(xerrors.ClassNotFound, "no such workflow version")
	}
	return runs.PinnedDefinition{
		VersionID: p.f.versionID, WorkflowID: uuid.Max, Version: 1,
		ContentHash: p.hash, Doc: p.doc,
	}, nil
}

// Repo reads the REAL repos row rather than answering from constants, which
// is what the production adapter does and what makes the mutable-repo half of
// revalidateAuthorization testable at all: a rename or a moved default branch
// has to be visible to the run for the refusal to have anything to refuse.
func (p fixturePorts) Repo(ctx context.Context, id uuid.UUID) (runs.RepoRef, error) {
	ref := runs.RepoRef{ID: id, ProjectID: p.f.projectID}
	err := p.f.pool.QueryRow(ctx,
		`SELECT forge_kind, slug, default_branch, credential_id FROM repos WHERE id = $1`, id,
	).Scan(&ref.Forge, &ref.Slug, &ref.DefaultBranch, &ref.CredentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return runs.RepoRef{}, xerrors.E(xerrors.ClassNotFound, "no such repo")
	}
	return ref, err
}

// Email is runs.PrincipalSource over the fixture's users table.
func (p fixturePorts) Email(ctx context.Context, userID uuid.UUID) (string, error) {
	var email string
	err := p.f.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, userID).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", xerrors.E(xerrors.ClassNotFound, "no such user")
	}
	return email, err
}

func (p fixturePorts) HarnessByName(_ context.Context, name string) (runs.HarnessRef, error) {
	if name != "cc" {
		return runs.HarnessRef{}, xerrors.E(xerrors.ClassNotFound, "no such harness")
	}
	return runs.HarnessRef{
		ID: p.harness, Kind: "claude-code",
		CredentialID: p.harnessCred, CredentialKind: "model_key",
	}, nil
}

func agenticDoc() runs.DefinitionDoc {
	return runs.DefinitionDoc{
		Nodes: []runs.DefNode{
			{ID: "plan", Kind: runs.KindPlan, Harness: "cc", Instructions: "Plan the change."},
			{ID: "impl", Kind: runs.KindImplement, Harness: "cc", Instructions: "Implement the plan."},
			{ID: "review", Kind: runs.KindReview, Harness: "cc", Instructions: "Review the patch."},
		},
		Edges: []runs.DefEdge{
			{From: "plan", To: "impl", Artifact: runs.ArtifactKindPlan},
			{From: "impl", To: "review", Artifact: runs.ArtifactKindPatch},
		},
	}
}

// gatedConveyorDoc is agenticDoc closed with the gate and the publish it
// authorizes — the shape Plan 05 refused at intake and Plan 08 executes.
func gatedConveyorDoc() runs.DefinitionDoc {
	doc := agenticDoc()
	doc.Nodes = append(doc.Nodes,
		runs.DefNode{ID: "gate", Kind: runs.KindGate, Revision: &runs.DefRevision{Target: "impl", MaxRounds: 2}},
		runs.DefNode{ID: "publish", Kind: runs.KindGitPublish},
	)
	doc.Edges = append(doc.Edges,
		runs.DefEdge{From: "impl", To: "gate", Artifact: runs.ArtifactKindPatch},
		runs.DefEdge{From: "review", To: "gate", Artifact: runs.ArtifactKindReviewVerdict},
		runs.DefEdge{From: "gate", To: "publish", Artifact: runs.ArtifactKindPatch},
	)
	return doc
}

func newService(t *testing.T, f fixture, doc runs.DefinitionDoc, snap runs.PolicySnapshot) (*runs.Service, fixturePorts) {
	t.Helper()
	return newServiceWithClock(t, f, doc, snap, clock.System{})
}

// newServiceWithClock is newService with the time seam left to the caller —
// for tests about what the app clock may and may not decide.
func newServiceWithClock(t *testing.T, f fixture, doc runs.DefinitionDoc,
	snap runs.PolicySnapshot, c clock.Clock,
) (*runs.Service, fixturePorts) {
	t.Helper()
	// A fixture with a real remote pins that remote's head; everything else
	// keeps the synthetic literal its assertions were written against.
	head := "basesha0000000000000000000000000000000000"
	if f.baseSHA != "" {
		head = f.baseSHA
	}
	forge := fake.NewForge(map[uuid.UUID]string{f.repoID: head})
	publisher := fake.NewPublisher(nil)
	if f.remote != "" {
		forge.Remote(f.repoID, f.remote)
		publisher.Remote(f.repoID, f.remote)
	}
	ports := fixturePorts{
		f: f, doc: doc, hash: "deadbeef", harness: uuid.Max, harnessCred: uuid.New(),
		forge: forge, publisher: publisher,
	}
	svc := runs.NewService(f.pool, c, audit.NewRecorder(c),
		ports, ports, ports, forge, publisher, ports, runs.Params{Snapshot: snap})
	return svc, ports
}

func principal(f fixture) authn.Principal {
	return authn.Principal{UserID: f.userID, Email: "spine@example.com", Role: authn.RoleAdmin}
}

func TestCreateTaskPinsEverything(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	task, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1,
		Title: "ship it", Inputs: map[string]any{"hint": "small"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ProjectID != f.projectID || task.LatestRunID != run.ID || task.LatestRunStatus != runs.RunPending {
		t.Errorf("task projection wrong: %+v", task)
	}
	if run.Status != runs.RunPending || run.DefinitionHash != "deadbeef" || !strings.HasPrefix(run.BaseSHA, "basesha") {
		t.Errorf("run pins wrong: %+v", run)
	}
	if run.Snapshot != runs.DefaultSnapshot() {
		t.Errorf("snapshot not pinned: %+v", run.Snapshot)
	}
	wantDeadline := run.CreatedAt.Add(24 * time.Hour)
	if run.WallDeadline.Sub(wantDeadline) > time.Second || wantDeadline.Sub(run.WallDeadline) > time.Second {
		t.Errorf("wall deadline %v, want ~%v", run.WallDeadline, wantDeadline)
	}

	// run.created event exists; no phases yet (orchestrator materializes).
	evs, err := svc.ListRunEvents(ctx, run.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != "run.created" {
		t.Errorf("events = %+v, want one run.created", evs)
	}
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 0 {
		t.Errorf("intake must not materialize phases, got %d", len(phases))
	}
}

func TestCreateTaskValidation(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name  string
		doc   runs.DefinitionDoc
		in    runs.NewTask
		field string
	}{
		{"empty title", agenticDoc(), runs.NewTask{RepoID: uuid.Max, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: ""}, "title"},
		{"title too long", agenticDoc(), runs.NewTask{RepoID: uuid.Max, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: strings.Repeat("x", 201)}, "title"},
		{"unknown harness", func() runs.DefinitionDoc { d := agenticDoc(); d.Nodes[0].Harness = "ghost"; return d }(), runs.NewTask{WorkflowVersion: 1, Title: "t"}, "harness"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newService(t, f, tc.doc, runs.DefaultSnapshot())
			in := tc.in
			if in.RepoID == uuid.Nil {
				in.RepoID = f.repoID
			}
			if in.WorkflowID == uuid.Nil {
				in.WorkflowID = uuid.Max
			}
			_, _, err := svc.CreateTask(context.Background(), principal(f), in)
			if xerrors.ClassOf(err) != xerrors.ClassInvalid && xerrors.ClassOf(err) != xerrors.ClassNotFound {
				t.Fatalf("err = %v, want invalid/not-found", err)
			}
			// Assert which field was blamed: without this, a case could
			// pass by being rejected for an unrelated reason.
			var xe *xerrors.Error
			if !errors.As(err, &xe) {
				t.Fatalf("err = %v, want a classified error", err)
			}
			blamed := false
			for _, fe := range xe.Fields {
				if fe.Field == tc.field {
					blamed = true
				}
			}
			if !blamed {
				t.Errorf("fields = %+v, want one blaming %q", xe.Fields, tc.field)
			}
		})
	}
}

// Plan 05 refused a definition containing a gate or a git-publish node at
// intake. Plan 08's compiler assigns the gate that authorizes the publish
// instead, so the refusal is gone and the whole conveyor must now pin a run
// like any other definition. Without this the removal is untested: the
// deleted "gate rejected" case asserted the old behaviour and nothing
// asserted the new one.
func TestCreateTaskAcceptsAGatedDefinition(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, gatedConveyorDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	task, run, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "ship it",
	})
	if err != nil {
		t.Fatalf("a gated definition must be accepted at intake: %v", err)
	}
	if run.Status != runs.RunPending || task.LatestRunID != run.ID {
		t.Errorf("gated run not pinned pending: run %+v, task %+v", run, task)
	}
	// Intake still materializes nothing; the gate node is the orchestrator's
	// business, and the run is a pending row like any other.
	_, phases, _, err := svc.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(phases) != 0 {
		t.Errorf("intake must not materialize phases, got %d", len(phases))
	}
}

func TestRerunSupersedesBeforeWork(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	_, run1, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}

	run2, err := svc.Rerun(ctx, principal(f), run1.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	got1, _, _, err := svc.GetRun(ctx, run1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got1.Status != runs.RunSuperseded {
		t.Errorf("run1 = %s, want superseded", got1.Status)
	}
	if run2.Status != runs.RunPending {
		t.Errorf("run2 = %s, want pending", run2.Status)
	}

	// A running run refuses: flip run2 to running and try again.
	if _, err := f.pool.Exec(ctx, `UPDATE runs SET status='running', started_at=now() WHERE id=$1`, run2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rerun(ctx, principal(f), run1.TaskID); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Errorf("rerun of running run: %v, want conflict", err)
	}
}

// countRows is the atomicity probe: intake either leaves a whole row
// graph behind or nothing at all.
func countRows(t *testing.T, f fixture, table string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCreateTaskIsOneTransaction is the invariant ListTasks' inner JOIN
// LATERAL rests on: a task without a run is unrepresentable. An invalid
// injected snapshot fails inside the transaction, after the task row is
// already inserted — the rollback must take the task with it.
func TestCreateTaskIsOneTransaction(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.PolicySnapshot{})
	ctx := context.Background()

	_, _, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "doomed",
	})
	if err == nil {
		t.Fatal("CreateTask with an invalid snapshot must fail")
	}
	// Guards the proof: the snapshot is rejected while minting the run,
	// which is inside the transaction and after the task row exists. A
	// refactor that validates it earlier would make the counts below
	// vacuous, so fail loudly instead.
	if !strings.Contains(err.Error(), "params snapshot") {
		t.Fatalf("expected the failure to come from insertRun, got %v", err)
	}
	for _, table := range []string{"tasks", "runs", "run_events", "outbox", "audit_entries"} {
		if n := countRows(t, f, table); n != 0 {
			t.Errorf("%s has %d rows after rollback, want 0", table, n)
		}
	}
}

// TestIntakeAudits pins the audit vocabulary intake writes; every entry
// is recorded in the same transaction as the rows it describes.
func TestIntakeAudits(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()

	_, run1, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rerun(ctx, principal(f), run1.TaskID); err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]int{"task.created": 1, "run.created": 2, "run.superseded": 1} {
		var n int
		if err := f.pool.QueryRow(ctx,
			`SELECT count(*) FROM audit_entries WHERE action = $1`, action).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s audited %d times, want %d", action, n, want)
		}
	}
	// The superseded run carries both its creation and its supersession.
	evs, err := svc.ListRunEvents(ctx, run1.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Kind != "run.created" || evs[1].Kind != "run.superseded" {
		t.Errorf("run1 events = %+v, want run.created then run.superseded", evs)
	}
}

func TestGrantScopedListing(t *testing.T) {
	f := newFixture(t)
	svc, _ := newService(t, f, agenticDoc(), runs.DefaultSnapshot())
	ctx := context.Background()
	if _, _, err := svc.CreateTask(ctx, principal(f), runs.NewTask{
		RepoID: f.repoID, WorkflowID: uuid.Max, WorkflowVersion: 1, Title: "t",
	}); err != nil {
		t.Fatal(err)
	}
	all, err := svc.ListTasks(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("ListTasks = %d, %v", len(all), err)
	}
	scoped, err := svc.ListTasksByProjects(ctx, []uuid.UUID{f.projectID})
	if err != nil || len(scoped) != 1 {
		t.Fatalf("scoped = %d, %v", len(scoped), err)
	}
	empty, err := svc.ListTasksByProjects(ctx, []uuid.UUID{uuid.Max})
	if err != nil || len(empty) != 0 {
		t.Fatalf("foreign project = %d, %v", len(empty), err)
	}
}
