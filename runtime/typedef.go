package runtime

import (
	"fmt"
	"go/ast"
	"strconv"
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
		// predeclared basic typedefs are global identities — a builtin
		// name built at different sites still names the same type, and
		// the aliases fold: byte is uint8, rune is int32. Declared
		// names (a `type T` decl) stay distinct objects.
		if basicTypedefName(a.Name) && basicTypedefName(b.Name) {
			return canonBasicName(a.Name) == canonBasicName(b.Name)
		}
		return false
	}
	return TypUnderlyingSpelling(a) == TypUnderlyingSpelling(b)
}

// basicTypeNames is the predeclared basic-type set — the names the
// builtin environment binds as KindNamedBasic.
var basicTypeNames = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "float32": true, "float64": true,
	"complex64": true, "complex128": true,
	"string": true, "bool": true, "byte": true, "rune": true,
}

// basicTypedefName reports whether name is a predeclared basic type —
// the typedefs whose identity is the name itself.
func basicTypedefName(name string) bool {
	return basicTypeNames[name]
}

// basicTypedefs are the shared basic-type typedefs handed to values
// materialized outside name resolution (host-unboxed slice elements) —
// one object per name keeps the a == b fast path and matches the
// builtin environment's typedefs under canonical-name identity. Built
// eagerly so reads are goroutine-safe by construction.
var basicTypedefs = func() map[string]*TypeDef {
	tds := make(map[string]*TypeDef, len(basicTypeNames))
	for name := range basicTypeNames {
		tds[name] = &TypeDef{Name: name, Kind: KindNamedBasic}
	}
	return tds
}()

// BasicTypedef returns the shared predeclared basic typedef for name —
// nil for non-basic names.
func BasicTypedef(name string) *TypeDef {
	return basicTypedefs[name]
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
	return typSpelling(e, ctx, false)
}

// typSpelling spells e like TypSpelling; under=true renders the
// underlying-type view instead, where channel direction is ignored
// (`chan T`, `<-chan T` and `chan<- T` share the underlying chan T).
func typSpelling(e ast.Expr, ctx *TypeDef, under bool) string {
	var binds map[string]Value
	var file *syntax.File
	var pkg *Package
	if ctx != nil {
		binds, file, pkg = ctx.Binds, ctx.File, ctx.Pkg
	}
	switch t := e.(type) {
	case *ast.Ident:
		if btd := boundTypedef(binds, t.Name); btd != nil {
			return typBoundSpellingU(btd, under)
		}
		if predeclaredTypeName(t.Name) {
			return canonBasicName(t.Name)
		}
		if pkg != nil {
			return pkg.Path + "." + t.Name
		}
		return canonBasicName(t.Name)
	case *ast.StarExpr:
		return "*" + typSpelling(t.X, ctx, under)
	case *ast.ArrayType:
		if t.Len != nil {
			return "[" + typLenName(t.Len) + "]" + typSpelling(t.Elt, ctx, under)
		}
		return "[]" + typSpelling(t.Elt, ctx, under)
	case *ast.Ellipsis:
		return "[]" + typSpelling(t.Elt, ctx, under)
	case *ast.MapType:
		return "map[" + typSpelling(t.Key, ctx, under) + "]" + typSpelling(t.Value, ctx, under)
	case *ast.ChanType:
		// direction is part of the type identity but not of the
		// underlying type: <-chan T, chan<- T and chan T are three
		// different types sharing the underlying chan T.
		if !under {
			switch t.Dir {
			case ast.RECV:
				return "<-chan " + typSpelling(t.Value, ctx, under)
			case ast.SEND:
				return "chan<- " + typSpelling(t.Value, ctx, under)
			}
		}
		return "chan " + typSpelling(t.Value, ctx, under)
	case *ast.ParenExpr:
		return typSpelling(t.X, ctx, under)
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if p := typImportPath(file, id.Name); p != "" {
				return p + "." + t.Sel.Name
			}
			// an unresolved selector qualifier is a package reference,
			// not a local type name — spell it literally. Type-name
			// spelling would prefix the enclosing package's path and
			// double-qualify synthesized exprs ("bytes.Buffer" coming
			// out "bytes.bytes.Buffer").
			return id.Name + "." + t.Sel.Name
		}
		return typSpelling(t.X, ctx, under) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return typSpelling(t.X, ctx, under) + "[" + typSpelling(t.Index, ctx, under) + "]"
	case *ast.IndexListExpr:
		s := typSpelling(t.X, ctx, under) + "["
		for i, x := range t.Indices {
			if i > 0 {
				s += ","
			}
			s += typSpelling(x, ctx, under)
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
			sb.WriteString(typSpelling(m.Type, ctx, under))
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
				sb.WriteString(typSpelling(f.Type, ctx, under))
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
		sb.WriteString(typFieldSpellings(t.Params, ctx, under))
		sb.WriteString(")")
		if res := typFieldSpellings(t.Results, ctx, under); res != "" {
			sb.WriteString("(")
			sb.WriteString(res)
			sb.WriteString(")")
		}
		return sb.String()
	}
	return fmt.Sprintf("%T", e)
}

// TypGoSpelling renders e the way Go's reflect.Type.String does —
// unlike TypSpelling (an identity key), the display form drops result
// parentheses for single results, writes variadics as `...T`, and pads
// interface/struct braces: `func(int) int`, `interface { M(int) string }`,
// `struct { x int "tag" }`. Names of params/results are dropped, and
// `a, b int` expands to `a int, b int` like the go/types printer.
func TypGoSpelling(e ast.Expr, ctx *TypeDef) string {
	var binds map[string]Value
	var file *syntax.File
	var pkg *Package
	if ctx != nil {
		binds, file, pkg = ctx.Binds, ctx.File, ctx.Pkg
	}
	switch t := e.(type) {
	case *ast.Ident:
		if btd := boundTypedef(binds, t.Name); btd != nil {
			return typBoundSpellingU(btd, false)
		}
		if predeclaredTypeName(t.Name) {
			return canonBasicName(t.Name)
		}
		if pkg != nil {
			// Go's Type.String qualifies by package NAME — the main
			// package of `module uc1` spells `main.Box`, not `uc1.Box`.
			// Synthesized composites carry the elem's identity name —
			// "pkg/path.T" — so a path-qualified ident loses its own
			// package prefix before requalifying by name.
			name := t.Name
			if pkg.Path != "" {
				name = strings.TrimPrefix(name, pkg.Path+".")
			}
			// host-bound packages carry no import path — their typedef
			// names are already name-qualified ("bytes.Buffer"), so a
			// requalifying wrapper strips the name prefix too, or it
			// would double ("bytes.bytes.Buffer").
			if pkg.Name != "" {
				name = strings.TrimPrefix(name, pkg.Name+".")
			}
			return pkg.Name + "." + name
		}
		return canonBasicName(t.Name)
	case *ast.StarExpr:
		return "*" + TypGoSpelling(t.X, ctx)
	case *ast.ArrayType:
		if t.Len != nil {
			return "[" + typLenName(t.Len) + "]" + TypGoSpelling(t.Elt, ctx)
		}
		return "[]" + TypGoSpelling(t.Elt, ctx)
	case *ast.Ellipsis:
		return "..." + TypGoSpelling(t.Elt, ctx)
	case *ast.MapType:
		return "map[" + TypGoSpelling(t.Key, ctx) + "]" + TypGoSpelling(t.Value, ctx)
	case *ast.ChanType:
		switch t.Dir {
		case ast.RECV:
			return "<-chan " + TypGoSpelling(t.Value, ctx)
		case ast.SEND:
			return "chan<- " + TypGoSpelling(t.Value, ctx)
		}
		return "chan " + TypGoSpelling(t.Value, ctx)
	case *ast.ParenExpr:
		return TypGoSpelling(t.X, ctx)
	case *ast.SelectorExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			if p := typImportPath(file, id.Name); p != "" {
				// Go qualifies by the imported package's clause name —
				// `net/http.Client` spells `http.Client`.
				return p[strings.LastIndex(p, "/")+1:] + "." + t.Sel.Name
			}
			// exprOf renders a named typedef's identity name as
			// `pkg/path . T`; display requalifies the path by the
			// declaring package's clause name, and imports by their
			// local alias.
			if pkg != nil && id.Name == pkg.Path {
				return pkg.Name + "." + t.Sel.Name
			}
			if file != nil {
				for _, im := range file.Imports {
					if im.Path == id.Name {
						alias := im.Alias
						if alias == "" || alias == "_" || alias == "." {
							alias = im.Path[strings.LastIndex(im.Path, "/")+1:]
						}
						return alias + "." + t.Sel.Name
					}
				}
			}
			// an unresolved ident qualifier is a package reference, not
			// a typename — requalifying it through the ident arm would
			// double the prefix (`bytes.bytes.Buffer` for bound pkgs).
			return id.Name + "." + t.Sel.Name
		}
		return TypGoSpelling(t.X, ctx) + "." + t.Sel.Name
	case *ast.IndexExpr:
		return TypGoSpelling(t.X, ctx) + "[" + TypGoSpelling(t.Index, ctx) + "]"
	case *ast.IndexListExpr:
		s := TypGoSpelling(t.X, ctx) + "["
		for i, x := range t.Indices {
			if i > 0 {
				s += ","
			}
			s += TypGoSpelling(x, ctx)
		}
		return s + "]"
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "interface {}"
		}
		var ms []string
		for _, m := range t.Methods.List {
			if len(m.Names) == 0 {
				// embedded interface — the type itself stands in
				ms = append(ms, TypGoSpelling(m.Type, ctx))
				continue
			}
			for _, n := range m.Names {
				if ft, ok := m.Type.(*ast.FuncType); ok {
					ms = append(ms, n.Name+goFuncSig(ft, ctx))
				}
			}
		}
		return "interface { " + strings.Join(ms, "; ") + " }"
	case *ast.StructType:
		if t.Fields == nil || len(t.Fields.List) == 0 {
			return "struct {}"
		}
		var fs []string
		for _, f := range t.Fields.List {
			typ := TypGoSpelling(f.Type, ctx)
			if f.Tag != nil {
				tag := f.Tag.Value
				if len(tag) >= 2 && tag[0] == '`' {
					tag = tag[1 : len(tag)-1]
				}
				typ += " " + strconv.Quote(tag)
			}
			if len(f.Names) == 0 {
				fs = append(fs, typ)
				continue
			}
			for _, n := range f.Names {
				fs = append(fs, n.Name+" "+typ)
			}
		}
		return "struct { " + strings.Join(fs, "; ") + " }"
	case *ast.FuncType:
		return "func" + goFuncSig(t, ctx)
	}
	return fmt.Sprintf("%T", e)
}

// goFuncSig renders a signature as `(params) results` with Go's spacing:
// params are `, `-joined types (names dropped), zero results render
// nothing, a single result goes unparenthesized, and several wrap in
// parens. `func(int) int` is `func` + `(int) int`.
func goFuncSig(t *ast.FuncType, ctx *TypeDef) string {
	var sb strings.Builder
	sb.WriteString("(")
	sb.WriteString(goFieldSpellings(t.Params, ctx))
	sb.WriteString(")")
	if t.Results == nil || len(t.Results.List) == 0 {
		return sb.String()
	}
	nr := 0
	for _, f := range t.Results.List {
		if n := len(f.Names); n > 0 {
			nr += n
		} else {
			nr++
		}
	}
	res := goFieldSpellings(t.Results, ctx)
	if nr > 1 {
		sb.WriteString(" (" + res + ")")
	} else {
		sb.WriteString(" " + res)
	}
	return sb.String()
}

// goFieldSpellings renders a signature field list like typFieldSpellings
// but with `, ` separators, for display rather than identity.
func goFieldSpellings(fl *ast.FieldList, ctx *TypeDef) string {
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
			parts = append(parts, TypGoSpelling(f.Type, ctx))
		}
	}
	return strings.Join(parts, ", ")
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
		return typSpelling(src, td, true)
	}
	return canonBasicName(td.Name)
}

// typBoundSpellingU spells an instantiated type argument: a named type
// keeps its declared identity (T=MyInt spells "pkg.MyInt", not "int"),
// an anonymous shape spells structurally. under=true renders its
// underlying type instead, since a type argument's underlying
// substitutes into the instantiated type's underlying.
func typBoundSpellingU(td *TypeDef, under bool) string {
	if under {
		return TypUnderlyingSpelling(td)
	}
	if td.Name != "" {
		if td.Pkg != nil {
			// a host-bound td's Name is already "pkgpath.Name"
			if strings.HasPrefix(td.Name, td.Pkg.Path+".") {
				return td.Name
			}
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
func typFieldSpellings(fl *ast.FieldList, ctx *TypeDef, under bool) string {
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
			parts = append(parts, typSpelling(f.Type, ctx, under))
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
