// Package arch encodes the layout law (decisions §9) as tests over the real
// import graph: bounded contexts at the top of internal/ are peers of
// platform/ (shared kernel) and app/ (composition root); cmd/ is thin.
// Violations fail CI; the fix is always the code, never the rule.
package arch

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const module = "github.com/giiorg/caesar/backend"

// Graph maps a package's import path to its direct imports.
type Graph map[string][]string

// nonContext segments under internal/ that are not bounded contexts.
var nonContext = map[string]bool{"platform": true, "app": true, "arch": true, "analyzers": true, "contracts": true}

// contextOf returns the bounded-context segment of a module package path.
func contextOf(pkg string) (string, bool) {
	rest, ok := strings.CutPrefix(pkg, module+"/internal/")
	if !ok {
		return "", false
	}
	seg, _, _ := strings.Cut(rest, "/")
	if seg == "" || nonContext[seg] {
		return "", false
	}
	return seg, true
}

func isPlatform(pkg string) bool {
	return pkg == module+"/internal/platform" || strings.HasPrefix(pkg, module+"/internal/platform/")
}

func isApp(pkg string) bool {
	return pkg == module+"/internal/app" || strings.HasPrefix(pkg, module+"/internal/app/")
}
func isCmd(pkg string) bool { return strings.HasPrefix(pkg, module+"/cmd/") }

func short(pkg string) string { return strings.TrimPrefix(pkg, module+"/") }

// CheckContextIsolation: a bounded context never imports another one.
func CheckContextIsolation(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		from, ok := contextOf(pkg)
		if !ok {
			continue
		}
		for _, imp := range imps {
			to, ok := contextOf(imp)
			if ok && to != from {
				out = append(out, fmt.Sprintf("%s imports %s: bounded contexts are peers and never import each other", short(pkg), short(imp)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// CheckPlatformPurity: the shared kernel imports no context and no app.
func CheckPlatformPurity(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		if !isPlatform(pkg) {
			continue
		}
		for _, imp := range imps {
			if _, isCtx := contextOf(imp); isCtx || isApp(imp) {
				out = append(out, fmt.Sprintf("%s imports %s: platform is the shared kernel and imports no business code", short(pkg), short(imp)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// CheckNoAppImports: contexts never import the composition root.
func CheckNoAppImports(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		if _, ok := contextOf(pkg); !ok {
			continue
		}
		for _, imp := range imps {
			if isApp(imp) {
				out = append(out, fmt.Sprintf("%s imports %s: contexts never import the composition root", short(pkg), short(imp)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// CheckCmdOnlyApp: cmd packages import only internal/app from the module.
func CheckCmdOnlyApp(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		if !isCmd(pkg) {
			continue
		}
		for _, imp := range imps {
			if strings.HasPrefix(imp, module+"/") && !isApp(imp) {
				out = append(out, fmt.Sprintf("%s imports %s: cmd packages import only internal/app", short(pkg), short(imp)))
			}
		}
	}
	sort.Strings(out)
	return out
}

// DenyList maps banned import prefixes to the locked decision behind them.
var DenyList = map[string]string{
	"github.com/stretchr/testify": "stdlib asserts + go-cmp only (spec §7)",
	"gorm.io":                     "no ORMs — pgx + sqlc (decisions §9)",
	"entgo.io":                    "no ORMs — pgx + sqlc (decisions §9)",
	"go.uber.org/fx":              "no DI frameworks — manual composition roots (decisions §9)",
	"go.uber.org/dig":             "no DI frameworks — manual composition roots (decisions §9)",
	"go.uber.org/zap":             "log/slog only (decisions §9)",
	"github.com/sirupsen/logrus":  "log/slog only (decisions §9)",
	"github.com/rs/zerolog":       "log/slog only (decisions §9)",
	"github.com/spf13/cobra":      "stdlib flag only (decisions §9)",
	"github.com/spf13/viper":      "strict JSON config via platform/config only (spec §8)",
	"github.com/joho/godotenv":    "env access only inside platform/config (spec §3)",
	"github.com/pkg/errors":       "stdlib errors + %w (decisions §9)",
}

// CheckDenyList: no module package imports a banned dependency.
func CheckDenyList(g Graph) []string {
	var out []string
	for pkg, imps := range g {
		for _, imp := range imps {
			for prefix, reason := range DenyList {
				if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
					out = append(out, fmt.Sprintf("%s imports %s: %s", short(pkg), imp, reason))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// normalize maps test-binary and external-test-package variants back to
// the package they test ("p.test", "p_test" → "p"), so synthetic test
// edges never masquerade as cross-context imports.
func normalize(path string) string {
	path = strings.TrimSuffix(path, ".test")
	return strings.TrimSuffix(path, "_test")
}

// LoadGraph builds the import graph of every package in the module,
// including integration- and property-tagged files and test files. The
// module-path pattern (not "./...") makes the load independent of the
// test's working directory (internal/arch), covering the whole module.
func LoadGraph() (Graph, error) {
	cfg := &packages.Config{
		Mode:       packages.NeedName | packages.NeedImports,
		Dir:        ".",
		Tests:      true,
		BuildFlags: []string{"-tags", "integration,property"},
	}
	pkgs, err := packages.Load(cfg, module+"/...")
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("arch: packages loaded with errors")
	}
	g := Graph{}
	for _, p := range pkgs {
		path := normalize(p.PkgPath)
		for imp := range p.Imports {
			imp = normalize(imp)
			g[path] = append(g[path], imp)
		}
	}
	return g, nil
}
