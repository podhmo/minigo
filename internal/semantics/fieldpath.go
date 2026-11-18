package semantics

import (
	"fmt"
	"go/ast"

	"github.com/podhmo/minigo/index"
)

// FieldPath records where a selector's field name lands inside a
// struct type: Index holds the successive positions in each nested
// struct's field list, counting one index per declared field name —
// reflect's Field.Index convention, so [i] is a direct field and
// [i j] is the field j of the embedded field i.
type FieldPath struct {
	Index []int
	// PkgPath is the package declaring the terminal field: an
	// unexported field is selectable only from that package.
	// Accessibility is the caller's check — the search itself is
	// export-unaware because an unexported field still participates
	// in Go's depth and ambiguity rules.
	PkgPath  string
	Exported bool
}

// fieldQueue is one BFS node: a resolved struct type and the index
// path that reaches it from the root.
type fieldQueue struct {
	ref  TypeRef // ref.Expr is a *ast.StructType
	path []int
	seen map[ast.Expr]bool // lookup exprs already resolved along this path
}

// FieldPathOf answers where the field name lands inside the struct
// type t names — the declaration-level half of Go's embedded-field
// selection (methods are a separate query, M4-2). Fields of t itself
// win over any promoted field; several candidates at the same shallow
// depth are ambiguous and come back as Failed. An embedded field is
// also a member under its unqualified type name. A package holding an
// embedded type yields Deferred when the policy forbids indexing it.
func (s *Service) FieldPathOf(t TypeRef, name string, p Policy) Result[FieldPath] {
	root := s.resolveStruct(t, p, map[ast.Expr]bool{})
	if root.Kind != Found {
		// t names no struct (a named non-struct type, an unresolvable
		// or bound type expression) — it has no fields to find.
		return Result[FieldPath]{Kind: root.Kind, Err: root.Err}
	}
	level := []fieldQueue{{ref: root.Val, seen: map[ast.Expr]bool{}}}
	for depth := 0; len(level) > 0 && depth < 32; depth++ {
		var hits []FieldPath
		var next []fieldQueue
		var deferred bool
		var failed error
		for _, q := range level {
			st, ok := q.ref.Expr.(*ast.StructType)
			if !ok || st.Fields == nil {
				continue
			}
			idx := 0
			for _, f := range st.Fields.List {
				if len(f.Names) == 0 {
					// an embedded field is itself a member under its
					// unqualified type name, and opens its subtree.
					if an := anonFieldName(f.Type); an == name {
						hits = append(hits, FieldPath{
							Index:    appendIdx(q.path, idx),
							PkgPath:  q.ref.Ctx.Pkg,
							Exported: ast.IsExported(an),
						})
					}
					// the child gets its own copy of the path's seen
					// set: a sibling's lookups must not prune its
					// subtree (diamond visits are legal).
					seen := make(map[ast.Expr]bool, len(q.seen)+1)
					for k := range q.seen {
						seen[k] = true
					}
					child := s.resolveStruct(TypeRef{Expr: f.Type, Ctx: q.ref.Ctx}, p, seen)
					switch child.Kind {
					case Found:
						next = append(next, fieldQueue{ref: child.Val, path: appendIdx(q.path, idx), seen: seen})
					case Deferred:
						deferred = true
					case Failed:
						if failed == nil {
							failed = child.Err
						}
					}
					idx++
					continue
				}
				for _, n := range f.Names {
					if n.Name == name {
						hits = append(hits, FieldPath{
							Index:    appendIdx(q.path, idx),
							PkgPath:  q.ref.Ctx.Pkg,
							Exported: n.IsExported(),
						})
					}
					idx++
				}
			}
		}
		if len(hits) > 0 {
			// a hit at this depth is final: nothing deeper can compete,
			// so deferred or failed subtrees cannot change the answer.
			if len(hits) > 1 {
				return Result[FieldPath]{Kind: Failed, Err: fmt.Errorf("ambiguous selector %s", name)}
			}
			return Result[FieldPath]{Kind: Found, Val: hits[0]}
		}
		if failed != nil {
			return Result[FieldPath]{Kind: Failed, Err: failed}
		}
		if deferred {
			// an unexplored subtree could hold the name at the shallowest
			// remaining depth — the answer cannot be given yet.
			return Result[FieldPath]{Kind: Deferred}
		}
		level = next
	}
	return Result[FieldPath]{Kind: Missing}
}

func appendIdx(path []int, i int) []int {
	out := make([]int, len(path)+1)
	copy(out, path)
	out[len(path)] = i
	return out
}

// resolveStruct reduces t to the struct type it names: named types
// resolve through their declaration to the underlying expression —
// fields are part of the underlying type, so defined types and aliases
// alike peel to it — and pointer, instantiation and paren wrappers
// unwrap. Found carries a TypeRef whose Expr is the *ast.StructType in
// its declaring context; Missing means the type names no struct (a
// named non-struct, a bound type parameter, an unresolvable
// expression); Deferred and Failed propagate the declaration lookup's
// answer. seen holds the type expressions already resolved along this
// path, closing embedded-type cycles without touching diamond visits.
func (s *Service) resolveStruct(t TypeRef, p Policy, seen map[ast.Expr]bool) Result[TypeRef] {
	expr := t.Expr
	for depth := 0; depth < 32; depth++ {
		switch x := expr.(type) {
		case *ast.StructType:
			// wrappers peeled along the way left t.Expr stale — the
			// caller asserts the resolved expr is the struct.
			t.Expr = expr
			return Result[TypeRef]{Kind: Found, Val: t}
		case *ast.ParenExpr:
			expr = x.X
		case *ast.StarExpr:
			expr = x.X
		case *ast.IndexExpr:
			expr = x.X
		case *ast.IndexListExpr:
			expr = x.X
		case *ast.Ident:
			if _, bound := t.Ctx.Binds[x.Name]; bound {
				// a type parameter's field set is per instantiation —
				// no static answer.
				return Result[TypeRef]{Kind: Missing}
			}
			r, ok := s.declOfExpr(t.Ctx, expr, p, seen)
			if !ok {
				return Result[TypeRef]{Kind: Missing}
			}
			if r.Kind != Found {
				return Result[TypeRef]{Kind: r.Kind, Err: r.Err}
			}
			t, expr = r.Val, r.Val.Expr
			// the binds are the caller's instantiation context —
			// they do not apply inside another declaration's body
			// (a plain type there may share a parameter's name).
			t.Ctx.Binds = nil
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			if !ok {
				return Result[TypeRef]{Kind: Missing}
			}
			path, pres := s.importPathOf(t.Ctx, id.Name, p)
			if pres.Kind != Found {
				return Result[TypeRef]{Kind: pres.Kind, Err: pres.Err}
			}
			if path == "" {
				return Result[TypeRef]{Kind: Missing}
			}
			r, ok := s.declOfExpr(DeclContext{Pkg: path, Binds: t.Ctx.Binds}, expr, p, seen)
			if !ok {
				return Result[TypeRef]{Kind: Missing}
			}
			if r.Kind != Found {
				return Result[TypeRef]{Kind: r.Kind, Err: r.Err}
			}
			t, expr = r.Val, r.Val.Expr
			t.Ctx.Binds = nil
		default:
			return Result[TypeRef]{Kind: Missing}
		}
	}
	return Result[TypeRef]{Kind: Missing}
}

// declOfExpr runs TypeDeclOf for the name expr spells, guarding the
// embedded-type cycle: ok is false when expr was already resolved on
// this path (invalid Go — embedded cycles are rejected upstream).
func (s *Service) declOfExpr(ctx DeclContext, expr ast.Expr, p Policy, seen map[ast.Expr]bool) (Result[TypeRef], bool) {
	if seen[expr] {
		return Result[TypeRef]{}, false
	}
	seen[expr] = true
	var name string
	switch x := expr.(type) {
	case *ast.Ident:
		name = x.Name
	case *ast.SelectorExpr:
		name = x.Sel.Name
	}
	return s.TypeDeclOf(ctx, name, p), true
}

// importPathOf resolves the package name id in ctx's import table to
// its path; "" when no import binds it. A no-alias import whose path
// basename does not spell id is still a candidate — the real package
// clause may name it differently — so its declared name is checked via
// the index (a load the policy does not allow defers the whole answer,
// since the embedded spec may be resolvable after all).
func (s *Service) importPathOf(ctx DeclContext, id string, p Policy) (string, Result[struct{}]) {
	if ctx.File == nil {
		return "", Result[struct{}]{Kind: Found}
	}
	var candidates []string
	for _, imp := range ctx.File.Imports {
		if imp.LocalName() == id {
			return imp.Path, Result[struct{}]{Kind: Found}
		}
		if imp.Alias == "" {
			candidates = append(candidates, imp.Path)
		}
	}
	for _, path := range candidates {
		ix := s.src.Indexed(path)
		if ix == nil {
			if p == IndexedOnly {
				return "", Result[struct{}]{Kind: Deferred}
			}
			var err error
			ix, err = s.src.Load(path)
			if err != nil {
				return "", Result[struct{}]{Kind: Failed, Err: err}
			}
			if ix == nil {
				return "", Result[struct{}]{Kind: Deferred}
			}
		}
		if packageNameOf(ix) == id {
			return path, Result[struct{}]{Kind: Found}
		}
	}
	return "", Result[struct{}]{Kind: Found}
}

// packageNameOf returns the name the package's files declare — the
// package clause, which may differ from the path basename.
func packageNameOf(ix *index.Index) string {
	for _, d := range ix.Decls {
		if d.File != nil && d.File.AST != nil && d.File.AST.Name != nil {
			return d.File.AST.Name.Name
		}
	}
	return ""
}

// anonFieldName derives the field name of an embedded struct field —
// the unqualified type name, ignoring pointers, packages and type
// arguments.
func anonFieldName(x ast.Expr) string {
	switch t := x.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return anonFieldName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return anonFieldName(t.X)
	case *ast.IndexListExpr:
		return anonFieldName(t.X)
	}
	return ""
}
