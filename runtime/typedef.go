package runtime

import (
	"fmt"
	"go/ast"
	"strings"

	"github.com/podhmo/minigo/syntax"
)

// ---- typedef identity ----
//
// Type identity is judged in many places — DeepEqual, `==` on interface
// values, conversions, type switches — and historically each site grew
// its own comparison (object identity here, name+package there, field
// lists elsewhere) with subtly different answers. The single entry point
// is TypIdentical / TypIdenticalStrict; each caller picks the strength
// its semantics need and the shared spelling machinery below does the
// work.

// TypIdentical reports whether two typedefs name the same type — the
// runtime's "same dynamic type" notion: the same decl object, named
// typedefs equal by name + package + instantiation binds, or anonymous
// typedefs with equal shape spellings (`[]int` never equals `[]string`,
// and `struct{X int}` never equals `struct{X any}`).
func TypIdentical(a, b *TypeDef) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil || a.Kind != b.Kind {
		return false
	}
	if a.Name != "" || b.Name != "" {
		return a.Name != "" && canonBasicName(a.Name) == canonBasicName(b.Name) && a.Pkg == b.Pkg && bindsEq(a.Binds, b.Binds)
	}
	if a.Anon != nil && b.Anon != nil {
		return TypSpelling(a.Anon, a) == TypSpelling(b.Anon, b)
	}
	return false
}

// TypIdenticalStrict is the DeepEqual-grade identity: named typedefs
// must be the same declaration object (two `type T` decls are distinct
// types even when they spell identically) and identity includes the
// underlying type's spelling, so anonymous shapes that share a Kind but
// differ underneath — `[]int` vs `[3]int`, `struct{X int}` vs
// `struct{Y int}` — stay distinct.
func TypIdenticalStrict(a, b *TypeDef) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil || a.Kind != b.Kind {
		return false
	}
	if a.Name != "" || b.Name != "" {
		return false
	}
	return TypUnderlyingSpelling(a) == TypUnderlyingSpelling(b)
}

// canonBasicName folds predeclared aliases: byte is uint8 and rune is
// int32 — an alias spelled at a call site and its canonical name are the
// same type.
func canonBasicName(n string) string {
	switch n {
	case "byte":
		return "uint8"
	case "rune":
		return "int32"
	}
	return n
}

// bindsEq compares generic instantiation bindings: `Wrap[int]` and
// `Wrap[string]` share Name+Pkg but instantiate differently — a declared
// type is identical only when its type arguments are.
func bindsEq(a, b map[string]Value) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !bindArgEq(av, bv) {
			return false
		}
	}
	return true
}

func bindArgEq(a, b Value) bool {
	at, aok := a.(*TypeDef)
	bt, bok := b.(*TypeDef)
	if aok != bok {
		return false
	}
	if !aok {
		return a == b
	}
	return TypIdentical(at, bt)
}

// ---- type spelling ----
//
// TypSpelling renders a type AST to its identity spelling in the context
// of ctx, the typedef the expression was written under: generic binds
// substitute bound type arguments, non-predeclared idents qualify by
// package path, and `a.T` selectors resolve through the declaring file's
// import table — so `[]Foo` written in two packages never collides. byte
// and rune normalize to canonical names so []byte and []uint8 spell
// identically. Struct, interface and func shapes spell their internals —
// field names, types, tags, signatures — since those decide identity.
// A nil ctx renders unqualified (a type written at top level).
func TypSpelling(e ast.Expr, ctx *TypeDef) string {
	var binds map[string]Value
	var file *syntax.File
	var pkg *Package
	if ctx != nil {
		binds, file, pkg = ctx.Binds, ctx.File, ctx.Pkg
	}
	switch t := e.(type) {
	case *ast.Ident:
		if btd := boundTypedef(binds, t.Name); btd != nil {
			return typBoundSpelling(btd)
		}
		if predeclaredTypeName(t.Name) {
			return canonBasicName(t.Name)
		}
		if pkg != nil {
			return pkg.Path + "." + t.Name
		}
		return canonBasicName(t.Name)
	case *ast.StarExpr:
		return "*" + TypSpelling(t.X, ctx)
	case *ast.ArrayType:
		if t.Len != nil {
			return "[" + typLenName(t.Len) + "]" + TypSpelling(t.Elt, ctx)
		}
		return "[]" + TypSpelling(t.Elt, ctx)
	case *ast.Ellipsis:
		return "[]" + TypSpelling(t.Elt, ctx)
	case *ast.MapType:
		return "map[" + TypSpelling(t.Key, ctx) + "]" + TypSpelling(t.Value, ctx)
	case *ast.ChanType:
		return "chan " + TypSpelling(t.Value, ctx)
	case *ast.ParenExpr:
		return TypSpelling(t.X, ctx)
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if p := typImportPath(file, id.Name); p != "" {
				return p + "." + t.Sel.Name
			}
		}
		return TypSpelling(t.X, ctx) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return TypSpelling(t.X, ctx) + "[" + TypSpelling(t.Index, ctx) + "]"
	case *ast.IndexListExpr:
		s := TypSpelling(t.X, ctx) + "["
		for i, x := range t.Indices {
			if i > 0 {
				s += ","
			}
			s += TypSpelling(x, ctx)
		}
		return s + "]"
	case *ast.InterfaceType:
		// methods decide identity, so spell them — an empty interface
		// still renders "interface{}".
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "interface{}"
		}
		var sb strings.Builder
		sb.WriteString("interface{")
		for _, m := range t.Methods.List {
			for _, n := range m.Names {
				sb.WriteString(n.Name)
			}
			sb.WriteString(TypSpelling(m.Type, ctx))
			sb.WriteString(";")
		}
		sb.WriteString("}")
		return sb.String()
	case *ast.StructType:
		// field names, types and tags all decide identity — an empty
		// struct still renders "struct{}".
		var sb strings.Builder
		sb.WriteString("struct{")
		if t.Fields != nil {
			for _, f := range t.Fields.List {
				for i, n := range f.Names {
					if i > 0 {
						sb.WriteString(",")
					}
					sb.WriteString(n.Name)
				}
				if len(f.Names) > 0 {
					sb.WriteString(" ")
				}
				sb.WriteString(TypSpelling(f.Type, ctx))
				if f.Tag != nil {
					sb.WriteString(" ")
					sb.WriteString(f.Tag.Value)
				}
				sb.WriteString(";")
			}
		}
		sb.WriteString("}")
		return sb.String()
	case *ast.FuncType:
		var sb strings.Builder
		sb.WriteString("func(")
		sb.WriteString(typFieldSpellings(t.Params, ctx))
		sb.WriteString(")")
		if res := typFieldSpellings(t.Results, ctx); res != "" {
			sb.WriteString("(")
			sb.WriteString(res)
			sb.WriteString(")")
		}
		return sb.String()
	}
	return fmt.Sprintf("%T", e)
}

// TypUnderlyingSpelling spells a typedef's underlying type — its Anon
// expression for anonymous/shape typedefs, else its decl Spec.Type, else
// the declared name.
func TypUnderlyingSpelling(td *TypeDef) string {
	if td == nil {
		return ""
	}
	src := td.Anon
	if src == nil && td.Spec != nil {
		src = td.Spec.Type
	}
	if src != nil {
		return TypSpelling(src, td)
	}
	return canonBasicName(td.Name)
}

// typBoundSpelling spells an instantiated type argument: a named type
// keeps its declared identity (T=MyInt spells "pkg.MyInt", not "int"),
// an anonymous shape spells structurally.
func typBoundSpelling(td *TypeDef) string {
	if td.Name != "" {
		if td.Pkg != nil {
			return td.Pkg.Path + "." + td.Name
		}
		return canonBasicName(td.Name)
	}
	src := td.Anon
	if src == nil && td.Spec != nil {
		src = td.Spec.Type
	}
	if src != nil {
		return TypSpelling(src, td)
	}
	return fmt.Sprintf("%p", td)
}

// boundTypedef returns the typedef a type parameter binds to, if any.
func boundTypedef(binds map[string]Value, name string) *TypeDef {
	if binds == nil {
		return nil
	}
	if bv, ok := binds[name]; ok {
		if btd, ok := bv.(*TypeDef); ok {
			return btd
		}
	}
	return nil
}

// typFieldSpellings renders a signature field list as its comma-joined
// type spellings — `a, b int` contributes `int,int` since parameter
// names are not part of a func type's identity.
func typFieldSpellings(fl *ast.FieldList, ctx *TypeDef) string {
	if fl == nil {
		return ""
	}
	var parts []string
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			parts = append(parts, TypSpelling(f.Type, ctx))
		}
	}
	return strings.Join(parts, ",")
}

// typLenName renders an array-length expression inside a type-identity
// spelling — the `3` of `[3]int`, a named const, or `...`.
func typLenName(e ast.Expr) string {
	switch l := e.(type) {
	case nil:
		return ""
	case *ast.BasicLit:
		return l.Value
	case *ast.Ident:
		return l.Name
	case *ast.Ellipsis:
		return "..."
	}
	return fmt.Sprintf("%T", e)
}

// predeclaredTypeName reports whether name is a predeclared type-ish
// identifier — basic types, aliases (byte, rune) and pseudo-types
// (error, any, comparable) — which never carry a package qualifier.
func predeclaredTypeName(name string) bool {
	switch name {
	case "bool", "string",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
		"byte", "rune", "float32", "float64", "complex64", "complex128",
		"error", "any", "comparable":
		return true
	}
	return false
}

// typImportPath resolves a file-local import alias (explicit or the
// basename-derived default) to its import path.
func typImportPath(file *syntax.File, alias string) string {
	if file == nil {
		return ""
	}
	for _, im := range file.Imports {
		if im.LocalName() == alias {
			return im.Path
		}
	}
	return ""
}
