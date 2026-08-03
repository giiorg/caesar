package arch

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"
)

// CheckDeadConfig verifies every leaf field of the named config struct is
// read at least once outside its defining package. Nested structs declared
// in the same package are recursed into; anything else is a leaf.
func CheckDeadConfig(dir, configPkgPath, configTypeName string) ([]string, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedImports | packages.NeedDeps,
		Dir:        dir,
		Tests:      true,
		BuildFlags: []string{"-tags", "integration"},
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	if packages.PrintErrors(pkgs) > 0 {
		return nil, fmt.Errorf("arch: packages loaded with errors")
	}

	// With Tests: true, a package with its own _test.go files loads as two
	// variants sharing one PkgPath: the plain package (ID == PkgPath, the
	// one every importer actually links against) and a separately
	// type-checked "pkg [pkg.test]" variant for its internal test binary.
	// Each variant has its own *types.Var per field, so matching on
	// PkgPath alone can bind configPkg to the test variant — whose field
	// objects never appear in any other package's TypesInfo.Uses, making
	// every field look dead. Require ID == PkgPath to pick the real one.
	var configPkg *packages.Package
	packages.Visit(pkgs, func(p *packages.Package) bool {
		if p.PkgPath == configPkgPath && p.ID == configPkgPath && p.Types != nil {
			configPkg = p
		}
		return true
	}, nil)
	if configPkg == nil {
		return nil, fmt.Errorf("arch: config package %s not found under %s", configPkgPath, dir)
	}

	obj := configPkg.Types.Scope().Lookup(configTypeName)
	if obj == nil {
		return nil, fmt.Errorf("arch: type %s not found in %s", configTypeName, configPkgPath)
	}
	st, ok := obj.Type().Underlying().(*types.Struct)
	if !ok {
		return nil, fmt.Errorf("arch: %s is not a struct", configTypeName)
	}

	fields := map[types.Object]string{} // leaf field object -> dotted path
	collectLeafFields(st, configTypeName, configPkg.Types, fields)

	used := map[types.Object]bool{}
	packages.Visit(pkgs, func(p *packages.Package) bool {
		// Skip the config package itself, packages without syntax, and
		// test variants: with Tests:true the plain package (ID == PkgPath)
		// excludes _test.go files, so restricting to it excludes test-only
		// reads wholesale ("pkg [pkg.test]" and "pkg_test" IDs differ).
		if p.PkgPath == configPkgPath || p.TypesInfo == nil || p.ID != p.PkgPath {
			return true
		}
		for _, file := range p.Syntax {
			nonReads := nonReadSelections(file)
			ast.Inspect(file, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				obj := p.TypesInfo.Uses[sel.Sel]
				if obj == nil || nonReads[sel.Sel] {
					return true
				}
				if _, tracked := fields[obj]; tracked {
					used[obj] = true
				}
				return true
			})
		}
		return true
	}, nil)

	var out []string
	for o, path := range fields {
		if !used[o] {
			out = append(out, fmt.Sprintf("config field %s has no read site outside the config package (no dead config)", path))
		}
	}
	sort.Strings(out)
	return out, nil
}

// nonReadSelections collects selector identifiers that appear in non-read
// positions: assignment left-hand sides and composite-literal keys. A field
// that only ever appears there has no read site (no dead config).
func nonReadSelections(file *ast.File) map[*ast.Ident]bool {
	out := map[*ast.Ident]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok {
					out[sel.Sel] = true
				}
			}
		case *ast.CompositeLit:
			for _, elt := range node.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.Ident); ok {
						out[key] = true
					}
				}
			}
		}
		return true
	})
	return out
}

func collectLeafFields(st *types.Struct, prefix string, home *types.Package, into map[types.Object]string) {
	for i := 0; i < st.NumFields(); i++ {
		f := st.Field(i)
		path := prefix + "." + f.Name()
		if named, ok := f.Type().(*types.Named); ok && named.Obj().Pkg() == home {
			if nested, ok := named.Underlying().(*types.Struct); ok {
				collectLeafFields(nested, path, home, into)
				continue
			}
		}
		into[f] = path
	}
}
