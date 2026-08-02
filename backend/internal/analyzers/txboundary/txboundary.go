// Package txboundary mechanically approximates "transactions never span
// bounded contexts" (spec §1 evolvability rule): no exported function,
// method, or interface method declared inside a bounded context may mention
// a pgx transaction type in its signature. Transactions stay an internal
// implementation detail of the context that opened them.
package txboundary

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

var Analyzer = &analysis.Analyzer{
	Name: "txboundary",
	Doc:  "forbids pgx transaction types in exported bounded-context signatures",
	Run:  run,
}

// nonContext are the internal/ segments that are not bounded contexts.
var nonContext = map[string]bool{"platform": true, "app": true, "arch": true, "analyzers": true}

// boundedContext extracts the context segment from a package path, if any.
func boundedContext(pkgPath string) (string, bool) {
	idx := strings.LastIndex(pkgPath, "internal/")
	if idx == -1 {
		return "", false
	}
	rest := pkgPath[idx+len("internal/"):]
	seg, _, _ := strings.Cut(rest, "/")
	if seg == "" || nonContext[seg] {
		return "", false
	}
	return seg, true
}

func run(pass *analysis.Pass) (any, error) {
	if _, ok := boundedContext(pass.Pkg.Path()); !ok {
		return nil, nil
	}
	for _, file := range pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch decl := n.(type) {
			case *ast.FuncDecl:
				if !decl.Name.IsExported() {
					return true
				}
				fn, ok := pass.TypesInfo.Defs[decl.Name].(*types.Func)
				if ok && signatureMentionsTx(fn.Type().(*types.Signature)) {
					pass.Reportf(decl.Name.Pos(), "transaction type in exported bounded-context API %s — transactions never span contexts", decl.Name.Name)
				}
			case *ast.TypeSpec:
				if !decl.Name.IsExported() {
					return true
				}
				iface, ok := pass.TypesInfo.TypeOf(decl.Type).(*types.Interface)
				if !ok {
					return true
				}
				for i := 0; i < iface.NumExplicitMethods(); i++ {
					m := iface.ExplicitMethod(i)
					if signatureMentionsTx(m.Type().(*types.Signature)) {
						// Report at the method, so the diagnostic lands on
						// the offending line.
						pass.Reportf(m.Pos(), "transaction type in exported bounded-context API %s.%s — transactions never span contexts", decl.Name.Name, m.Name())
					}
				}
			}
			return true
		})
	}
	return nil, nil
}

var txTypePkgs = map[string]bool{
	"github.com/jackc/pgx/v5":         true,
	"github.com/jackc/pgx/v5/pgxpool": true,
}

func signatureMentionsTx(sig *types.Signature) bool {
	seen := map[types.Type]bool{}
	check := func(tuple *types.Tuple) bool {
		for i := 0; i < tuple.Len(); i++ {
			if mentionsTx(tuple.At(i).Type(), seen) {
				return true
			}
		}
		return false
	}
	return check(sig.Params()) || check(sig.Results())
}

func mentionsTx(t types.Type, seen map[types.Type]bool) bool {
	if seen[t] {
		return false
	}
	seen[t] = true
	switch tt := t.(type) {
	case *types.Named:
		obj := tt.Obj()
		if obj.Pkg() != nil && txTypePkgs[obj.Pkg().Path()] && obj.Name() == "Tx" {
			return true
		}
		return false
	case *types.Pointer:
		return mentionsTx(tt.Elem(), seen)
	case *types.Slice:
		return mentionsTx(tt.Elem(), seen)
	case *types.Array:
		return mentionsTx(tt.Elem(), seen)
	case *types.Map:
		return mentionsTx(tt.Key(), seen) || mentionsTx(tt.Elem(), seen)
	case *types.Chan:
		return mentionsTx(tt.Elem(), seen)
	case *types.Signature:
		return signatureMentionsTx(tt)
	}
	return false
}
