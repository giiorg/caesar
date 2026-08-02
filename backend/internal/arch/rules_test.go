package arch

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

const m = "github.com/giiorg/caesar/backend"

func TestCheckContextIsolation_FlagsCrossContextImports(t *testing.T) {
	g := Graph{
		m + "/internal/execution":        {m + "/internal/identity", m + "/internal/platform/pg"},
		m + "/internal/identity":         {m + "/internal/platform/config"},
		m + "/internal/execution/worker": {m + "/internal/execution"},
	}
	got := CheckContextIsolation(g)
	want := []string{"internal/execution imports internal/identity: bounded contexts are peers and never import each other"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCheckContextIsolation_AllowsPlatformAndIntraContext(t *testing.T) {
	g := Graph{
		m + "/internal/execution":       {m + "/internal/platform/pg", m + "/internal/execution/queue"},
		m + "/internal/execution/queue": {m + "/internal/platform/pg"},
	}
	if got := CheckContextIsolation(g); len(got) != 0 {
		t.Fatalf("want clean, got %v", got)
	}
}

func TestCheckPlatformPurity_FlagsPlatformImportingContextOrApp(t *testing.T) {
	g := Graph{
		m + "/internal/platform/pg":    {m + "/internal/execution"},
		m + "/internal/platform/httpx": {m + "/internal/app"},
	}
	got := CheckPlatformPurity(g)
	if len(got) != 2 {
		t.Fatalf("want 2 violations, got %v", got)
	}
}

func TestCheckNoAppImports_FlagsContextImportingApp(t *testing.T) {
	g := Graph{
		m + "/internal/identity": {m + "/internal/app"},
	}
	if got := CheckNoAppImports(g); len(got) != 1 {
		t.Fatalf("want 1 violation, got %v", got)
	}
}

func TestCheckCmdOnlyApp_FlagsCmdImportingAnythingButApp(t *testing.T) {
	g := Graph{
		m + "/cmd/caesar": {m + "/internal/app", "os"},
		m + "/cmd/rogue":  {m + "/internal/platform/pg", "fmt"},
	}
	got := CheckCmdOnlyApp(g)
	want := []string{"cmd/rogue imports internal/platform/pg: cmd packages import only internal/app"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCheckDenyList_FlagsBannedDependencies(t *testing.T) {
	g := Graph{
		m + "/internal/identity":    {"github.com/stretchr/testify/require"},
		m + "/internal/app":         {"go.uber.org/zap"},
		m + "/internal/platform/pg": {"github.com/jackc/pgx/v5"},
	}
	got := CheckDenyList(g)
	if len(got) != 2 {
		t.Fatalf("want 2 violations (testify, zap), got %v", got)
	}
}
