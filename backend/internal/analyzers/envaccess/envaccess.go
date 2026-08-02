// Package envaccess enforces the constructed-env rule (spec §3): the process
// environment may only be read inside internal/platform/config. Everything
// an agent container sees is an explicitly constructed allowlist, so ambient
// env reads anywhere else are a designed-out bug class.
package envaccess

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "envaccess",
	Doc:  "forbids process-environment access outside internal/platform/config",
	Run:  run,
}

var forbidden = map[string]bool{
	"os.Getenv":       true,
	"os.LookupEnv":    true,
	"os.Environ":      true,
	"os.ExpandEnv":    true,
	"syscall.Getenv":  true,
	"syscall.Environ": true,
}

func run(pass *analysis.Pass) (any, error) {
	if strings.HasSuffix(pass.Pkg.Path(), "internal/platform/config") {
		return nil, nil
	}
	for _, file := range pass.Files {
		filename := pass.Fset.File(file.Pos()).Name()
		if strings.HasSuffix(filename, "_test.go") {
			continue // t.Setenv etc. are fine in tests
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			fn, ok := pass.TypesInfo.Uses[sel.Sel].(*types.Func)
			if !ok {
				return true
			}
			if forbidden[fn.FullName()] {
				pass.Reportf(call.Pos(), "environment access outside internal/platform/config (constructed env allowlist rule): %s", fn.FullName())
			}
			return true
		})
	}
	return nil, nil
}
