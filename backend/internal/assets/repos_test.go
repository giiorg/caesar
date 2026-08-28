// In-package: repoInUse is unexported, and the branch that matters here is the
// one no fixture can reach — a repo cannot hold publish records without also
// holding tasks (see repoInUse's comment), so an integration test would only
// ever exercise the tasks arm. This is where the other arm is pinned.
package assets

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/giiorg/caesar/backend/internal/platform/xerrors"
)

func TestRepoInUse_NamesWhatHoldsTheRepo(t *testing.T) {
	// Every FK that references repos.id, spelled as the migrations declare it.
	// A rename in a later migration must break this test, not the message.
	cases := []struct {
		constraint string
		want       string
	}{
		{"tasks_repo_id_fkey", "this repository is used by existing tasks"},
		{"approved_patches_repo_id_fkey", "this repository has publish records; it cannot be deleted"},
		{"publish_intents_repo_id_fkey", "this repository has publish records; it cannot be deleted"},
		// A constraint this function has never heard of still refuses, and
		// still refuses as a conflict rather than falling through to a 500.
		{"repos_some_future_fkey", "this repository has publish records; it cannot be deleted"},
	}
	for _, tc := range cases {
		err := repoInUse(tc.constraint)
		if xerrors.ClassOf(err) != xerrors.ClassConflict {
			t.Errorf("%s: class = %v, want conflict", tc.constraint, xerrors.ClassOf(err))
		}
		if got := err.Error(); got != tc.want {
			t.Errorf("%s: message = %q, want %q", tc.constraint, got, tc.want)
		}
	}
}

// The RESTRICT errors this package classifies arrive as 23001, not the 23503
// most Go code expects; fkViolation accepts both, and DeleteRepo's whole
// behaviour rests on the constraint name surviving that extraction.
func TestFkViolation_CarriesTheConstraintNameOffARestrictError(t *testing.T) {
	restrict := error(&pgconn.PgError{Code: "23001", ConstraintName: "tasks_repo_id_fkey"})
	if got, ok := fkViolation(restrict); !ok || got != "tasks_repo_id_fkey" {
		t.Fatalf("fkViolation(23001) = %q, %v; want the constraint name", got, ok)
	}
	wrapped := errors.Join(errors.New("assets: delete repo"), restrict)
	if got, ok := fkViolation(wrapped); !ok || got != "tasks_repo_id_fkey" {
		t.Fatalf("fkViolation(wrapped) = %q, %v; want the constraint name", got, ok)
	}
	if _, ok := fkViolation(&pgconn.PgError{Code: "23505", ConstraintName: "repos_project_id_forge_kind_slug_key"}); ok {
		t.Fatal("a unique violation must not read as a foreign-key violation")
	}
}
