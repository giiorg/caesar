//go:build integration

package workflows_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/giiorg/caesar/backend/internal/platform/authn"
	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
	"github.com/giiorg/caesar/backend/internal/workflows"
)

func TestCreateWorkflow_PersistsAndAudits(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)

	w, err := svc.CreateWorkflow(ctx, admin, "conveyor", "the month-one loop")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetWorkflow(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "conveyor" || got.LatestVersion != 0 {
		t.Fatalf("got %+v", got)
	}
	if n := auditCount(t, pool, "workflow.created"); n != 1 {
		t.Fatalf("workflow.created audited %d times", n)
	}

	if _, err := svc.CreateWorkflow(ctx, admin, "conveyor", ""); xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := svc.CreateWorkflow(ctx, admin, "", ""); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("empty name: %v", err)
	}
}

func TestCreateVersion_WriteDoorValidatesNumbersAndDedupes(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, err := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	if err != nil {
		t.Fatal(err)
	}

	v1, created, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first"))
	if err != nil || !created || v1.Version != 1 {
		t.Fatalf("v1: %+v created=%v err=%v", v1, created, err)
	}
	v2, created, err := svc.CreateVersion(ctx, admin, w.ID, docWith("second"))
	if err != nil || !created || v2.Version != 2 {
		t.Fatalf("v2: %+v created=%v err=%v", v2, created, err)
	}

	// Same content again — idempotent, no new row, no new audit entry.
	again, created, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first"))
	if err != nil || created || again.Version != 1 || again.ContentHash != v1.ContentHash {
		t.Fatalf("idempotent post: %+v created=%v err=%v", again, created, err)
	}
	if n := auditCount(t, pool, "workflow.version_created"); n != 2 {
		t.Fatalf("version_created audited %d times, want 2", n)
	}

	// Invalid document → classified, field-addressed.
	var xe *xerrors.Error
	_, _, err = svc.CreateVersion(ctx, admin, w.ID,
		[]byte(`{"schema": 1, "nodes": [{"id": "impl", "type": "implement", "instructions": "x"}]}`))
	if !errors.As(err, &xe) || xe.Class != xerrors.ClassInvalid {
		t.Fatalf("invalid doc: %v", err)
	}
	found := false
	for _, f := range xe.Fields {
		if f.Field == "nodes[0].harness" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violation fields missing nodes[0].harness: %+v", xe.Fields)
	}

	if _, _, err := svc.CreateVersion(ctx, admin, w.ID, []byte(`not json`)); xerrors.ClassOf(err) != xerrors.ClassInvalid {
		t.Fatalf("malformed json: %v", err)
	}
	ghost := seedActor(t, pool, "x@example.com", authn.RoleAdmin)
	if _, _, err := svc.CreateVersion(ctx, ghost, ghost.UserID, docWith("first")); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("unknown workflow: %v", err)
	}
}

func TestCreateVersion_ConcurrentWritersNeverShareANumber(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, err := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	if err != nil {
		t.Fatal(err)
	}

	const writers = 4
	versions := make([]int32, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith(string(rune('a'+i))))
			if err != nil {
				t.Error(err)
				return
			}
			versions[i] = v.Version
		}(i)
	}
	wg.Wait()
	seen := map[int32]bool{}
	for _, v := range versions {
		if v < 1 || v > writers || seen[v] {
			t.Fatalf("version numbers not unique/contiguous: %v", versions)
		}
		seen[v] = true
	}
}

func TestGetVersion_HashRecomputesFromStoredDocument(t *testing.T) {
	// jsonb storage re-normalizes bytes; the pinned invariant is that
	// parse(stored document) still hashes to the stored content_hash.
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, _ := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	made, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetVersion(ctx, w.ID, made.Version)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := workflows.ParseDocument(got.Document)
	if err != nil {
		t.Fatalf("stored document failed to parse: %v", err)
	}
	if doc.Hash() != got.ContentHash {
		t.Fatalf("recomputed hash %s != stored %s", doc.Hash(), got.ContentHash)
	}
	if _, err := svc.GetVersion(ctx, w.ID, 99); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("unknown version: %v", err)
	}
}

func TestGetVersionByID_ReturnsDocumentOrNotFound(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, _ := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	made, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetVersionByID(ctx, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != made.ID || got.WorkflowID != w.ID || got.Version != made.Version || got.ContentHash != made.ContentHash {
		t.Fatalf("version = %+v, want identity of %+v", got, made)
	}
	if len(got.Document) == 0 {
		t.Fatal("document must be returned")
	}
	if doc, err := workflows.ParseDocument(got.Document); err != nil || doc.Hash() != got.ContentHash {
		t.Fatalf("stored document must reparse to the stored hash: %v", err)
	}

	if _, err := svc.GetVersionByID(ctx, uuid.New()); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("unknown id: %v, want not_found", err)
	}
}

func TestListVersions_MetadataOnlyNewestFirst(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, _ := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	if _, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("second")); err != nil {
		t.Fatal(err)
	}

	vs, err := svc.ListVersions(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].Version != 2 || vs[1].Version != 1 {
		t.Fatalf("got %+v", vs)
	}
	if vs[0].Document != nil {
		t.Fatal("list results must not carry documents")
	}
	if lst, err := svc.ListWorkflows(ctx); err != nil || len(lst) != 1 || lst[0].LatestVersion != 2 {
		t.Fatalf("ListWorkflows: %+v err=%v", lst, err)
	}
}

func TestUpdateAndDeleteWorkflow_CascadeAndAudit(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	admin := seedActor(t, pool, "admin@example.com", authn.RoleAdmin)
	w, _ := svc.CreateWorkflow(ctx, admin, "conveyor", "")
	if _, _, err := svc.CreateVersion(ctx, admin, w.ID, docWith("first")); err != nil {
		t.Fatal(err)
	}

	name := "conveyor-v2"
	upd, err := svc.UpdateWorkflow(ctx, admin, w.ID, workflows.WorkflowUpdate{Name: &name})
	if err != nil || upd.Name != "conveyor-v2" {
		t.Fatalf("update: %+v err=%v", upd, err)
	}
	if err := svc.DeleteWorkflow(ctx, admin, w.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM workflow_versions").Scan(&left); err != nil || left != 0 {
		t.Fatalf("versions left after cascade: %d err=%v", left, err)
	}
	for _, action := range []string{"workflow.updated", "workflow.deleted"} {
		if n := auditCount(t, pool, action); n != 1 {
			t.Fatalf("%s audited %d times", action, n)
		}
	}
	if err := svc.DeleteWorkflow(ctx, admin, w.ID); xerrors.ClassOf(err) != xerrors.ClassNotFound {
		t.Fatalf("second delete: %v", err)
	}
}

func TestDeleteWorkflowBlockedByRuns(t *testing.T) {
	svc, pool, _ := newTestService(t)
	ctx := context.Background()
	actor := seedActor(t, pool, "blk@example.com", authn.RoleAdmin)

	wf, err := svc.CreateWorkflow(ctx, actor, "spine-block", "")
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := svc.CreateVersion(ctx, actor, wf.ID, docWith("blocker"))
	if err != nil {
		t.Fatal(err)
	}

	// A task referencing the version, via raw SQL: workflows never
	// imports the runs context (arch law), and the FK chain is what is
	// under test, not the spine service.
	mustScan := func(row pgx.Row) uuid.UUID {
		var id uuid.UUID
		if err := row.Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	projectID := mustScan(pool.QueryRow(ctx, `INSERT INTO projects (name) VALUES ('blk') RETURNING id`))
	credID := mustScan(pool.QueryRow(ctx, `INSERT INTO credentials (id, name, kind, key_id, wrapped_dek, ciphertext) VALUES (uuidv7(),'blk','forge_token','dev','\x00','\x00') RETURNING id`))
	repoID := mustScan(pool.QueryRow(ctx, `INSERT INTO repos (project_id, forge_kind, slug, credential_id) VALUES ($1,'github','blk/repo',$2) RETURNING id`, projectID, credID))
	mustScan(pool.QueryRow(ctx,
		`INSERT INTO tasks (project_id, repo_id, workflow_version_id, title, requester_id, created_at)
		 VALUES ($1, $2, $3, 'blocker', $4, now()) RETURNING id`,
		projectID, repoID, v.ID, actor.UserID))

	err = svc.DeleteWorkflow(ctx, actor, wf.ID)
	if xerrors.ClassOf(err) != xerrors.ClassConflict {
		t.Fatalf("delete with runs = %v, want conflict", err)
	}
	if _, err := svc.GetWorkflow(ctx, wf.ID); err != nil {
		t.Fatalf("workflow must survive the blocked delete: %v", err)
	}
}
