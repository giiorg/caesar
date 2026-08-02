package arch

import "testing"

func TestArchitecture_RealImportGraphIsClean(t *testing.T) {
	g, err := LoadGraph()
	if err != nil {
		t.Fatal(err)
	}
	if len(g) == 0 {
		t.Fatal("empty graph: LoadGraph found no packages")
	}
	for name, check := range map[string]func(Graph) []string{
		"context isolation":    CheckContextIsolation,
		"platform purity":      CheckPlatformPurity,
		"no app imports":       CheckNoAppImports,
		"cmd thinness":         CheckCmdOnlyApp,
		"dependency deny-list": CheckDenyList,
	} {
		if violations := check(g); len(violations) != 0 {
			t.Errorf("%s violated:\n  %v", name, violations)
		}
	}
}
