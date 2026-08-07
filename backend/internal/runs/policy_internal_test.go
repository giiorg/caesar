// In-package: snapshotFromRow is the read path's private tolerance for an
// older stored document, and the upgrade it performs has no exported name.
package runs

import "testing"

func TestDefaultSnapshot_AllowsSelfApprovalOnTheShippedBaseline(t *testing.T) {
	s := DefaultSnapshot()
	if s.SchemaVersion != 2 {
		t.Fatalf("schema = %d, want 2", s.SchemaVersion)
	}
	if !s.AllowSelfApproval {
		t.Error("the shipped baseline is solo-friendly (spec §6): self-approval defaults on")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("the shipped baseline must validate: %v", err)
	}
}

func TestSnapshotFromRow_UpgradesASchema1DocumentToSelfApprovalOn(t *testing.T) {
	// Runs minted before Plan 08 pinned a schema-1 snapshot with no such
	// field. Refusing to load them would strand every in-flight run at the
	// upgrade; defaulting to the shipped baseline value is the only answer
	// that changes no behaviour for them.
	got, err := snapshotFromRow([]byte(`{"schema":1,"max_attempts":3,"execution_budget_ms":1000,"wall_clock_ms":2000}`))
	if err != nil {
		t.Fatalf("schema 1 must still load: %v", err)
	}
	if !got.AllowSelfApproval {
		t.Error("an upgraded schema-1 snapshot must allow self approval")
	}
	if got.SchemaVersion != 2 {
		t.Errorf("schema = %d, want the document upgraded to 2", got.SchemaVersion)
	}
	if got.MaxAttempts != 3 || got.ExecutionBudgetMS != 1000 || got.WallClockMS != 2000 {
		t.Errorf("upgrade must not touch the pinned values: %+v", got)
	}
}

func TestSnapshotFromRow_RefusesAnUnknownSchema(t *testing.T) {
	if _, err := snapshotFromRow([]byte(`{"schema":99,"max_attempts":3}`)); err == nil {
		t.Fatal("an unknown snapshot schema is corruption, not a default")
	}
}
