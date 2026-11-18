package minigo

import (
	"go/ast"
	"go/token"
	"slices"
	"strconv"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// universeTypeName is the set of predeclared type names — universe-block
// identifiers, so an embedded `int` or `any` is the same type in every
// package and takes no package lock.
var universeTypeName = map[string]bool{
	"bool": true, "byte": true, "rune": true, "string": true,
	"error": true, "any": true, "comparable": true,
	"complex64": true, "complex128": true,
	"float32": true, "float64": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true,
}

// anonLeafSpelling finishes the spelling of a package-resolved anonymous
// typedef (typeid.go's td.Anon != nil && td.Pkg != nil branch): re-spells
// leaf names through the local/alias/bound typedefs they resolve to and
// appends the `#name@pkg` locks that pin embedded unexported fields.
func (e *Engine) anonLeafSpelling(td *runtime.TypeDef, s string) string {
	if s == "" {
		return ""
	}
	// A leaf naming a function-local type has to spell that
	// decl's identity: the display name is bare, so two
	// functions' same-named `type L` — or one local L bound to
	// different type arguments across instantiations — would
	// collide on it. Unused locals key as before, and a member
	// or field/parameter name is never a type reference.
	sels := map[*ast.Ident]bool{}
	embedded := map[string]bool{}
	ast.Inspect(td.Anon, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			sels[x.Sel] = true
		case *ast.Field:
			for _, nm := range x.Names {
				sels[nm] = true
			}
		case *ast.StructType:
			if x.Fields == nil {
				break
			}
			for _, f := range x.Fields.List {
				if len(f.Names) != 0 {
					continue
				}
				// an embedded field's name is the type name as
				// written — `struct{A}` is not `struct{T}` even
				// when A is an alias for T, so the leaf keeps
				// its spelled name.
				te := ast.Expr(f.Type)
				for {
					switch w := te.(type) {
					case *ast.ParenExpr:
						te = w.X
					case *ast.StarExpr:
						te = w.X
					default:
						goto unwrapped
					}
				}
			unwrapped:
				if id, ok := te.(*ast.Ident); ok {
					embedded[id.Name] = true
				}
			}
		}
		return true
	})
	used := map[string]*runtime.TypeDef{}
	ast.Inspect(td.Anon, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && !sels[id] {
			if lt := td.LocalTypes[id.Name]; lt != nil {
				used[id.Name] = lt
			} else if bv, ok := td.Binds[id.Name].(*runtime.TypeDef); ok && bv != nil {
				// a type parameter is transparent: `[]T` under
				// T=int IS `[]int`, so the bound typedef spells
				// the leaf itself.
				used[id.Name] = bv
			} else if td.Pkg.Index != nil {
				// a package-level alias peels like a local one:
				// `struct{a}` spells its target, not `pkg.a`.
				if info, ok := td.Pkg.Index.Types[id.Name]; ok && info.Decl != nil {
					if ts, isTs := info.Decl.Spec.(*ast.TypeSpec); isTs && ts.Assign.IsValid() {
						used[id.Name] = &runtime.TypeDef{Kind: runtime.KindAlias, Anon: ts.Type, Spec: ts, Pkg: td.Pkg, File: info.Decl.File}
					}
				}
			}
		}
		return true
	})
	ids := map[string]bool{}
	if len(used) > 0 {
		// Re-spell with the used leafs resolved: a local alias
		// unfolds to its target (`[]A` is `[]int`) and a declared
		// local qualifies by decl site; a declared leaf bound to
		// instantiation arguments also suffixes its own key,
		// which spells the binds.
		ctx := *td
		ctx.LocalTypes = make(map[string]*runtime.TypeDef, len(td.LocalTypes))
		for n, lt := range td.LocalTypes {
			ctx.LocalTypes[n] = lt
		}
		for n, lt := range used {
			if lt.Kind == runtime.KindAlias {
				if embedded[n] {
					if !token.IsExported(n) {
						// a package-level alias gets no
						// LocalTypes leaf of its own — seed
						// it so the embedded name peels to
						// its target in spelling exactly
						// like a function-local alias.
						ctx.LocalTypes[n] = lt
						// handled by the package-locked
						// suffix below.
						continue
					}
					// embedded alias: the name stays in the
					// spelling, and the key is the name plus
					// the shared target — two `type A = int`
					// decls name one field type.
					ids[n+"="+string(e.typeIDOf(lt))] = true
					continue
				}
				if tgt := e.peelAliasTd(lt); tgt != nil {
					ctx.LocalTypes[n] = tgt
				}
				continue
			}
			if _, bound := td.Binds[n]; td.LocalTypes[n] == nil && bound {
				// a bound type parameter substitutes its
				// argument's own spelling — no name survives.
				ctx.LocalTypes[n] = lt
				continue
			}
			cl := lt.CloneUncached()
			if lt.Spec != nil {
				cl.Name = n + "@" + strconv.FormatInt(int64(lt.Spec.Pos()), 10)
			}
			ctx.LocalTypes[n] = cl
			ids[n+"="+string(e.typeIDOf(lt))] = true
		}
		if is := runtime.TypGoSpelling(td.Anon, &ctx); is != "" {
			s = is
		}
	}
	// An unexported embedded name declared in a package is
	// locked to it: `struct{a}` from `type a = int` in one
	// package is not `struct{a}` from a same-named package at
	// another import path, so the key qualifies the name with
	// the declaring package's path — the leaf's own package
	// when the leaf resolved (a bound name locks to the
	// argument's package), td's otherwise. Universe-scope
	// names (`int`, `any`, `error`) are the same type in every
	// package and take no lock.
	for n := range embedded {
		if token.IsExported(n) {
			continue
		}
		lt := used[n]
		pkg := td.Pkg.Path
		if lt != nil && lt.Pkg != nil {
			pkg = lt.Pkg.Path
		}
		q := n + "@" + pkg
		if lt != nil {
			// name + package + target: function-local
			// `a = int` and `a = string` in the same package
			// embed different fields.
			ids[q+"="+string(e.typeIDOf(lt))] = true
			continue
		}
		if td.Pkg.Index != nil {
			if info, ok := td.Pkg.Index.Types[n]; ok && info.Decl != nil {
				// the package declares the name itself: a
				// package alias spells its target like a
				// function-local one (name+pkg+target), a
				// declared type is already pinned by
				// name+pkg — one name per package.
				if ts, isTs := info.Decl.Spec.(*ast.TypeSpec); isTs && ts.Assign.IsValid() {
					if tgt, err := e.resolveTypeRef(td, ts.Type); err == nil && tgt != nil {
						ids[q+"="+string(e.typeIDOf(tgt))] = true
					} else {
						ids[q] = true
					}
				} else {
					ids[q] = true
				}
				continue
			}
		}
		if universeTypeName[n] {
			continue
		}
		ids[q] = true
	}
	if len(ids) > 0 {
		leafs := make([]string, 0, len(ids))
		for k := range ids {
			leafs = append(leafs, k)
		}
		slices.Sort(leafs)
		s += "#" + strings.Join(leafs, ";")
	}
	return s
}
