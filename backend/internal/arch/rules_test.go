package arch

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

const m = "github.com/giiorg/caesar/backend"

func TestNormalize_StripsTestVariants(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{m + "/internal/identity", m + "/internal/identity"},
		{m + "/internal/identity_test", m + "/internal/identity"},
		{m + "/internal/identity.test", m + "/internal/identity"},
		{m + "/internal/identity_test.test", m + "/internal/identity"},
	}
	for _, tt := range tests {
		if got := normalize(tt.input); got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCheckContextIsolation_AllowsNormalizedExternalTestPackages(t *testing.T) {
	// Regression test: external test packages like identity_test are normalized
	// to their base context (identity) in LoadGraph, so a synthetic graph with
	// normalized paths should allow identity importing identity_test because both
	// normalize to the same context.
	g := Graph{
		m + "/internal/identity": {m + "/internal/identity", m + "/internal/platform/pg"},
	}
	if got := CheckContextIsolation(g); len(got) != 0 {
		t.Fatalf("want clean (identity and identity_test are same context after normalization), got %v", got)
	}
}

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
	want := []string{
		"internal/platform/httpx imports internal/app: platform is the shared kernel and imports no business code",
		"internal/platform/pg imports internal/execution: platform is the shared kernel and imports no business code",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCheckNoAppImports_FlagsContextImportingApp(t *testing.T) {
	g := Graph{
		m + "/internal/identity": {m + "/internal/app"},
	}
	got := CheckNoAppImports(g)
	want := []string{"internal/identity imports internal/app: contexts never import the composition root"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
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
	want := []string{
		"internal/app imports go.uber.org/zap: log/slog only (decisions §9)",
		"internal/identity imports github.com/stretchr/testify/require: stdlib asserts + go-cmp only (spec §7)",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}
