package arch

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestCheckDeadConfig_FlagsFieldsWithoutReadSites(t *testing.T) {
	got, err := CheckDeadConfig("testdata/deadconfig-module", "example.com/deadconfig/config", "Config")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"config field Config.OnlyAssigned has no read site outside the config package (no dead config)",
		"config field Config.OnlyLiteralKey has no read site outside the config package (no dead config)",
		"config field Config.OnlyTestRead has no read site outside the config package (no dead config)",
		"config field Config.Unused has no read site outside the config package (no dead config)",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func TestCheckDeadConfig_LiveConfigHasNoDeadFields(t *testing.T) {
	// "../.." = the backend module root: the checker loads "./..." under
	// dir, and this test runs with cwd internal/arch.
	got, err := CheckDeadConfig("../..", "github.com/giiorg/caesar/backend/internal/platform/config", "Config")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dead config fields:\n  %v", got)
	}
}
