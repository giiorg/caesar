// Package stubfake forces stubs onto the typed sentinel: ad-hoc
// "not implemented" strings in errors.New/fmt.Errorf/panic must become
// platform/xerrors.NotImplemented so callers can errors.Is on it and no
// stub can masquerade as domain failure (decisions §6.13).
package stubfake

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "stubfake",
	Doc:  "requires platform/xerrors.NotImplemented for not-implemented stubs",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	if strings.HasSuffix(pass.Pkg.Path(), "platform/xerrors") {
		return nil, nil // the sentinel's own definition site
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			if !isFlagged(pass, call) {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			if strings.Contains(strings.ToLower(lit.Value), "not implemented") {
				pass.Reportf(call.Pos(), "use platform/xerrors.NotImplemented — stubs must return the typed sentinel, never ad-hoc strings")
			}
			return true
		})
	}
	return nil, nil
}

// isFlagged reports whether call is errors.New, fmt.Errorf, or builtin panic.
func isFlagged(pass *analysis.Pass, call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		_, isBuiltin := pass.TypesInfo.Uses[fun].(*types.Builtin)
		return isBuiltin && fun.Name == "panic"
	case *ast.SelectorExpr:
		fn, ok := pass.TypesInfo.Uses[fun.Sel].(*types.Func)
		if !ok {
			return false
		}
		full := fn.FullName()
		return full == "errors.New" || full == "fmt.Errorf"
	}
	return false
}
