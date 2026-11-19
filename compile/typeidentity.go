package compile

import (
	"go/ast"
	"go/token"

	"github.com/podhmo/minigo/runtime"
)

// namedTypID is a named type's compile-time identity: two operand types
// with different ids are different named types, which gc never assigns
// to each other (both named — the comparison is a compile error).
type namedTypID struct {
	id   any    // comparable identity: *ast.TypeSpec or a string
	name string // source spelling for the trap message
}

// namedIdentOf resolves a type expression to a named type's identity:
// a local or package-level declared type (the spec pointer — two
// distinct `type Tag string` decls never alias), an alias resolves to
// its target, a qualified `pkg.T` to "path.T", and a predeclared type to
// its canonical name (byte→uint8, rune→int32 — the same type in gc).
// Type parameters, interface types, and unnamed forms report none.
func (c *compiler) namedIdentOf(te ast.Expr) (namedTypID, bool) {
	return c.namedIdentOfDepth(te, 8)
}

func (c *compiler) namedIdentOfDepth(te ast.Expr, depth int) (namedTypID, bool) {
	if depth == 0 {
		return namedTypID{}, false
	}
	switch t := ast.Unparen(te).(type) {
	case *ast.Ident:
		if t.NamePos == token.NoPos && predeclaredTypeNames[t.Name] {
			// a synthesized predeclared name — `int` for a range
			// index, `rune` for a string element, `bool` for
			// comma-ok — means the builtin itself: a decl like
			// `type int uint8` cannot rename it. `any`/`error` stay
			// unnamed — interface types report none.
			if t.Name == "any" || t.Name == "error" {
				return namedTypID{}, false
			}
			return namedTypID{id: "predecl:" + runtime.CanonicalBasicName(t.Name), name: t.Name}, true
		}
		info, found := c.resolveName(t.Name)
		if !found || !info.isType || info.iface {
			return namedTypID{}, false
		}
		if info.td != nil {
			return namedTypID{}, false // a bound type parameter — opaque
		}
		if info.tspec != nil {
			if info.tspec.Assign.IsValid() {
				return c.namedIdentOfDepth(info.tspec.Type, depth-1) // alias — the target's identity
			}
			return namedTypID{id: info.tspec, name: t.Name}, true
		}
		// predeclared named types: byte and rune canonicalize to their
		// underlying types — they are the same type for assignability.
		return namedTypID{id: "predecl:" + runtime.CanonicalBasicName(t.Name), name: t.Name}, true
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return namedTypID{}, false
		}
		ref, isPkg := c.importRef(id.Name)
		if !isPkg {
			return namedTypID{}, false
		}
		if p, ts := c.selTypeSpec(ref, t.Sel.Name); ts != nil && ts.Assign.IsValid() {
			scoped := c.scopeSpecType(p, specFileOf(p, ts.Pos()), ts.Type)
			return c.namedIdentOfDepth(scoped, depth-1)
		}
		if c.selIsIface(ref, t.Sel.Name) {
			// an interface operand compares dynamically — `rc ==
			// NoBody` is legal gc regardless of the other side.
			return namedTypID{}, false
		}
		name := id.Name
		if _, synth := c.synthImports[id.Name]; synth {
			// a minted qualifier has no source spelling — the
			// package clause's name is what gc prints.
			name = c.pkgDispName(ref)
		}
		return namedTypID{id: ref.Path + "." + t.Sel.Name, name: name + "." + t.Sel.Name}, true
	case *ast.StarExpr:
		// a pointer's identity wraps its base's — `i == &d` reads
		// as int vs *D, `&d == &e` as *D vs *E. Other composite
		// forms stay unnamed, keeping the gate conservative.
		inner, ok := c.namedIdentOfDepth(t.X, depth-1)
		if !ok {
			return namedTypID{}, false
		}
		return namedTypID{id: ptrTypID{elem: inner.id}, name: "*" + inner.name}, true
	}
	return namedTypID{}, false
}

// ptrTypID is the compile-time identity of a pointer to a named type:
// *D and *E are distinct types to gc even when D and E share an
// underlying, so the element's identity is part of the pointer's.
type ptrTypID struct {
	elem any
}
