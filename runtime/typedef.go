package runtime

import (
	"fmt"
	"go/ast"
	"strconv"
	"strings"
	"sync"

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
		if a.Local || b.Local {
			// Every func-local `type` declaration has its own
			// identity: same-named locals in different scopes are
			// distinct types, so identity is the declaration site
			// (Spec) plus instantiation binds — never name+package.
			return a.Spec != nil && a.Spec == b.Spec && bindsEq(a.Binds, b.Binds)
		}
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
		// a func-local `type` decl shadows everything above the block:
		// an alias spells its target (C is int32, not a pkg-qualified
		// C — two same-named local aliases never collide) and a declared
		// local type is unique to its decl site.
		if ctx != nil && ctx.LocalTypes != nil {
			if ltd := ctx.LocalTypes[t.Name]; ltd != nil {
				return typLocalSpelling(ltd, under)
			}
		}
		if btd := boundTypedef(binds, t.Name); btd != nil {
			return typBoundSpellingU(btd, under)
		}
		// `any` IS interface{} — spell the expansion so a func(any) any
		// signature identifies with func(interface{}) interface{}.
		if t.Name == "any" {
			return "interface{}"
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
		// a variadic param is not a slice: func(...int) and func([]int)
		// are different types. (Array `...` lengths spell through
		// typLenName, never reaching here.)
		return "..." + typSpelling(t.Elt, ctx, under)
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
		// struct still renders "struct{}". An embedded field's name is
		// its unqualified type name (`struct{Int}` and `struct{int}` are
		// different types even though Int is an int alias), so embeds
		// spell `derivedName type` like declared fields.
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
				if len(f.Names) == 0 {
					sb.WriteString(anonFieldName(f.Type))
				}
				sb.WriteString(" ")
				sb.WriteString(typSpelling(f.Type, ctx, under))
				if tag := structTagKey(f.Tag); tag != "" {
					sb.WriteString(" ")
					sb.WriteString(tag)
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

// typLocalSpelling spells a func-local typedef for identity. A local
// alias names its target type — `type C = int32` written in two
// scopes differs only in its target — while a declared local type is
// unique to its decl site: two func-local `type D`s never identify, so
// the spec position qualifies the package spelling. Under the
// underlying view both spell their declared body.
func typLocalSpelling(td *TypeDef, under bool) string {
	if td.Anon != nil && (under || td.Kind == KindAlias) {
		return typSpelling(td.Anon, td, under)
	}
	name := td.Name
	if td.Spec != nil {
		name += "@" + strconv.FormatInt(int64(td.Spec.Pos()), 10)
	}
	if td.Pkg != nil {
		return td.Pkg.Path + "." + name
	}
	return name
}

// anonFieldName derives the field name of an embedded struct field —
// the unqualified type name, ignoring pointers, packages and type
// args. compile.embedFieldName wraps this for the compiler.
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

// AnonFieldName exposes anonFieldName to the compiler.
func AnonFieldName(x ast.Expr) string {
	return anonFieldName(x)
}

// structTagKey normalizes a field tag literal for identity spellings:
// an absent tag and an empty tag literal are the same type (Go treats
// a zero-length tag as no tag — $GOROOT/test/fixedbugs/issue15439.go),
// and quote styles fold, so a backquoted tag and a double-quoted one
// spell alike. Returns "" when the field carries no identity-relevant
// tag.
func structTagKey(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	s, err := strconv.Unquote(tag.Value)
	if err != nil || s == "" {
		return ""
	}
	return strconv.Quote(s)
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
		btd := boundTypedef(binds, t.Name)
		if btd == nil && ctx != nil {
			btd = ctx.LocalTypes[t.Name]
		}
		if btd != nil && ctx != nil && len(ctx.OuterSpell) > 0 && btd.Local && len(btd.OuterArgs) == 0 {
			// a function-local decl referenced inside an instantiated
			// generic function spells with the enclosing args —
			// `main.L0[int]` — display only; identity lives on the
			// runtime-declared clone.
			bc := *btd
			bc.OuterArgs = ctx.OuterSpell
			btd = &bc
		}
		if btd != nil {
			// display, not identity: a bound argument qualifies by the
			// package's clause name like every other Type.String path —
			// typBoundSpellingU's Pkg.Path qualifier is for identity
			// spelling (<dir>/x.Point would leak the synthetic path).
			s := DisplayName(btd)
			if ctx != nil && ctx.inInstArgs && btd.Gen > 0 {
				s += "\u00b7" + strconv.Itoa(btd.Gen)
			}
			return s
		}
		// Go's Type.String expands the any alias — func(any) any
		// displays as func(interface {}) interface {}.
		if t.Name == "any" {
			return "interface {}"
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
				// `net/http.Client` spells `http.Client`, and an
				// `import o "x/odd"` whose package declares `package
				// weird` spells `weird.T`, not the alias or the path
				// basename.
				if name := importClauseName(pkg, file, id.Name); name != "" {
					return name + "." + t.Sel.Name
				}
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
		return instHead(t.X, ctx) + "[" + outerSpellPrefix(t.X, ctx) + instArgGoSpelling(t.Index, ctx) + "]" + instArgSuffix(t.X, ctx)
	case *ast.IndexListExpr:
		s := instHead(t.X, ctx) + "[" + outerSpellPrefix(t.X, ctx)
		for i, x := range t.Indices {
			if i > 0 {
				s += ","
			}
			s += instArgGoSpelling(x, ctx)
		}
		return s + "]" + instArgSuffix(t.X, ctx)
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

// FuncSigOf returns the declared signature of a function value — the
// type the value carries when stored in an interface: a method
// expression (T.M / (*T).M surfaces as a bare *Function) signs with
// its receiver as the first parameter, a bound method without it. The
// package, file imports and type binds spell the signature's names.
// Every nil on failure — builtins, nil members, non-func values — so
// the caller picks its fallback.
func FuncSigOf(v Value) (*ast.FuncType, *Package, *syntax.File, map[string]Value) {
	switch x := v.(type) {
	case *Function:
		if sig := declFuncSig(x); sig != nil {
			return sig, x.Pkg, x.File, x.Binds
		}
	case *Closure:
		if sig := declFuncSig(x.Fn); sig != nil {
			return sig, x.Fn.Pkg, x.Fn.File, x.Fn.Binds
		}
	case *BoundMethod:
		if x.Fn != nil && x.Fn.Decl != nil && x.Fn.Decl.Type != nil {
			return x.Fn.Decl.Type, x.Fn.Pkg, x.Fn.File, x.Fn.Binds
		}
	}
	return nil, nil, nil, nil
}

// declFuncSig returns the signature a declared function carries as a
// value: a method declaration signs with the receiver prepended (a bare
// Function value only surfaces as a method expression — BoundMethod
// takes the receiver-less path in FuncSigOf).
func declFuncSig(fn *Function) *ast.FuncType {
	if fn == nil || fn.Decl == nil || fn.Decl.Type == nil {
		return nil
	}
	if fn.Decl.Recv == nil || len(fn.Decl.Recv.List) == 0 {
		return fn.Decl.Type
	}
	params := []*ast.Field{fn.Decl.Recv.List[0]}
	if fn.Decl.Type.Params != nil {
		params = append(params, fn.Decl.Type.Params.List...)
	}
	return &ast.FuncType{Params: &ast.FieldList{List: params}, Results: fn.Decl.Type.Results}
}

// FuncGoSpelling renders a script function value's declared signature
// the way Go's reflect.Type.String does — `func(int) int`, `func()
// error` — resolving names through the function's own package, file
// imports and type binds (an instantiated `func(T) T` spells its bound
// argument). ok=false when the value carries no declaration to spell —
// plain builtins, nil members — so each caller picks its own fallback.
func FuncGoSpelling(v Value) (string, bool) {
	sig, pkg, file, binds := FuncSigOf(v)
	if sig == nil {
		return "", false
	}
	return TypGoSpelling(sig, &TypeDef{Pkg: pkg, File: file, Binds: binds}), true
}

// TypeResolver feeds the semantic signature comparator the engine
// lookups a typedef can need — alias peeling, interface requirement
// expansion, expression resolution inside a declaring context, and
// element typedefs for AST-less containers. Callers pass their own
// hook adapter (the VM's engine hooks, the reflect facade's Hooks);
// a nil resolver is legal and degrades every normalization to the
// identity spelling, so the compare never produces a false positive
// it would not have without one.
type TypeResolver interface {
	// PeelAlias follows `type A = B` chains to the aliased target;
	// resolvers without alias support return td unchanged.
	PeelAlias(td *TypeDef) *TypeDef
	// IfaceReqs and IfaceSigs expand an interface typedef's flattened
	// requirement set — names, and the signature-carrying members.
	// An error reports "unavailable", folding the compare back to
	// identity spelling.
	IfaceReqs(td *TypeDef) (map[string]bool, error)
	IfaceSigs(td *TypeDef) (map[string]*Function, error)
	// ResolveType resolves a signature expr in `from`'s declaring
	// context; (nil, nil) means "unresolvable", falling the compare
	// back to spelling.
	ResolveType(from *TypeDef, x ast.Expr) (*TypeDef, error)
	// ElemOf resolves the element typedef of a container carrying no
	// AST; (nil, nil) means "unresolvable".
	ElemOf(td *TypeDef) (*TypeDef, error)
}

// SigTypEq reports whether two typedefs name the same type where a
// signature spells them — TypIdentical's judgment plus the
// normalizations a spelling cannot see: an alias resolves to its
// target (`type A = int` IS int), anonymous interfaces compare as
// method SETS (member order and the any/interface{} spelling never
// decide identity, and embedded requirements flatten into the set),
// and composite elements — slice and array elems, map keys and
// values, chan elems, pointer pointees, struct fields, func params
// and results, instantiation args — compare recursively so
// normalization reaches any depth. Named declared types keep
// TypIdentical's decl-site identity; anything unresolvable falls back
// to the identity spelling, so the judgment never gets weaker than
// the spelling compare it replaces.
func SigTypEq(a, b *TypeDef, res TypeResolver) bool {
	return sigComparer{res: orResolver(res)}.sigTypEq(a, b)
}

// SigIdentical compares two members' declared signatures — an interface
// requirement and the concrete method offered against it. Either side
// lacking a decl signature (synthesized shims) satisfies by name. The
// single entry behind the VM's satisfaction checks and the reflect
// facade's Implements, so alias and anonymous-interface normalization
// reach both callers.
func SigIdentical(req, dyn *Function, res TypeResolver) bool {
	return sigComparer{res: orResolver(res)}.sigIdentical(req, dyn)
}

// SigMemo remembers SigIdentical verdicts per (requirement, offered)
// member pair. Interface satisfaction compares the same pairs over and
// over — every conversion of a *ast.Ident to ast.Expr re-checks End(),
// Pos() and exprNode() — and each fresh compare allocates typedef
// shells and walks both signatures. The verdict is a function of the two
// Functions alone (their decl, package, file and binds) as long as the
// resolver answers the same for one engine, so a memo shared by that
// engine's VMs is safe; a Function copied with new binds is a new key.
// The zero value is ready to use and safe for concurrent use.
type SigMemo struct{ m sync.Map }

type sigMemoKey struct{ req, dyn *Function }

// Identical is SigIdentical through the memo. A nil memo compares
// without remembering.
func (s *SigMemo) Identical(req, dyn *Function, res TypeResolver) bool {
	if s == nil || req == nil || dyn == nil {
		return SigIdentical(req, dyn, res)
	}
	k := sigMemoKey{req, dyn}
	if ok, hit := s.m.Load(k); hit {
		return ok.(bool)
	}
	ok := SigIdentical(req, dyn, res)
	s.m.Store(k, ok)
	return ok
}

// sigComparer carries the resolver the semantic signature comparator
// consults, keeping the compare's method shape.
type sigComparer struct{ res TypeResolver }

// noResolver answers "unavailable" for every lookup — the comparator's
// spellings-only baseline.
type noResolver struct{}

func (noResolver) PeelAlias(td *TypeDef) *TypeDef                   { return td }
func (noResolver) IfaceReqs(*TypeDef) (map[string]bool, error)      { return nil, errNoResolver }
func (noResolver) IfaceSigs(*TypeDef) (map[string]*Function, error) { return nil, errNoResolver }
func (noResolver) ResolveType(*TypeDef, ast.Expr) (*TypeDef, error) { return nil, nil }
func (noResolver) ElemOf(*TypeDef) (*TypeDef, error)                { return nil, nil }

var errNoResolver = fmt.Errorf("no type resolver")

func orResolver(res TypeResolver) TypeResolver {
	if res == nil {
		return noResolver{}
	}
	return res
}

// sigIdentical compares two members' declared signatures — an interface
// requirement and the concrete method offered against it. Either side
// lacking a decl signature (synthesized shims) satisfies by name.
func (c sigComparer) sigIdentical(req, dyn *Function) bool {
	if req == nil || dyn == nil || req.Decl == nil || dyn.Decl == nil || req.Decl.Type == nil || dyn.Decl.Type == nil {
		return true
	}
	rt := &TypeDef{Kind: KindFunc, Anon: req.Decl.Type, Pkg: req.Pkg, File: req.File, Binds: req.Binds}
	dt := &TypeDef{Kind: KindFunc, Anon: dyn.Decl.Type, Pkg: dyn.Pkg, File: dyn.File, Binds: dyn.Binds}
	return c.sigTypEq(rt, dt)
}

// sigTypEq reports whether two typedefs name the same type where a
// signature spells them — TypIdentical's judgment plus the
// normalizations a spelling cannot see: an alias resolves to its
// target (`type A = int` IS int), anonymous interfaces compare as
// method SETS (member order and the any/interface{} spelling never
// decide identity, and embedded requirements flatten into the set),
// and composite elements — slice and array elems, map keys and
// values, chan elems, pointer pointees, struct fields, func params
// and results, instantiation args — compare recursively so
// normalization reaches any depth. Named declared types keep
// TypIdentical's decl-site identity; anything unresolvable falls back
// to the identity spelling, so the judgment never gets weaker than
// the spelling compare it replaces.
func (c sigComparer) sigTypEq(a, b *TypeDef) bool {
	a = c.res.PeelAlias(a)
	b = c.res.PeelAlias(b)
	if a == b {
		return true
	}
	if a == nil || b == nil || a.Kind != b.Kind {
		return false
	}
	if a.Kind == KindInterface {
		return c.ifaceTypEq(a, b)
	}
	if a.Name != "" || b.Name != "" {
		return c.namedTypEq(a, b)
	}
	aa, bb := sigAnonOf(a), sigAnonOf(b)
	if aa == nil || bb == nil {
		return TypIdentical(a, b)
	}
	switch a.Kind {
	case KindFunc:
		fa, oka := aa.(*ast.FuncType)
		fb, okb := bb.(*ast.FuncType)
		if !oka || !okb {
			return TypIdentical(a, b)
		}
		return c.sigFieldListEq(fa.Params, a, fb.Params, b) &&
			c.sigFieldListEq(fa.Results, a, fb.Results, b)
	case KindSlice:
		at, oka := aa.(*ast.ArrayType)
		bt, okb := bb.(*ast.ArrayType)
		if !oka || !okb {
			// an element-carrying typedef has no length — it never
			// identifies with an array.
			if ar, ok := aa.(*ast.ArrayType); ok && ar.Len != nil {
				return false
			}
			if ar, ok := bb.(*ast.ArrayType); ok && ar.Len != nil {
				return false
			}
			return c.sigElemEq(a, b)
		}
		if (at.Len == nil) != (bt.Len == nil) {
			return false // []T and [N]T are different types
		}
		if at.Len != nil && !sigLenEq(at.Len, bt.Len) {
			return false
		}
		return c.sigExprEq(at.Elt, a, bt.Elt, b)
	case KindMap:
		am, oka := aa.(*ast.MapType)
		bm, okb := bb.(*ast.MapType)
		if !oka || !okb {
			return TypIdentical(a, b)
		}
		return c.sigExprEq(am.Key, a, bm.Key, b) && c.sigExprEq(am.Value, a, bm.Value, b)
	case KindChan:
		ac, oka := aa.(*ast.ChanType)
		bc, okb := bb.(*ast.ChanType)
		if !oka || !okb {
			return c.sigElemEq(a, b)
		}
		// direction is part of chan identity — <-chan T, chan<- T and
		// chan T are three different types.
		return ac.Dir == bc.Dir && c.sigExprEq(ac.Value, a, bc.Value, b)
	case KindPointer:
		ap, oka := aa.(*ast.StarExpr)
		bp, okb := bb.(*ast.StarExpr)
		if !oka || !okb {
			return c.sigElemEq(a, b)
		}
		return c.sigExprEq(ap.X, a, bp.X, b)
	case KindStruct:
		as, oka := aa.(*ast.StructType)
		bs, okb := bb.(*ast.StructType)
		if !oka || !okb {
			return TypIdentical(a, b)
		}
		return c.structTypEq(as, a, bs, b)
	}
	return TypIdentical(a, b)
}

// sigAnonOf returns the type expression a typedef spells — its Anon,
// or the declared underlying type for a Spec-carrying typedef.
func sigAnonOf(td *TypeDef) ast.Expr {
	if td.Anon != nil {
		return td.Anon
	}
	if td.Spec != nil {
		return td.Spec.Type
	}
	return nil
}

// ifaceTypEq compares two interface typedefs as method SETS: member
// order never decides identity, embedded requirements flatten through
// the engine's requirement hooks, and the predeclared `any` IS the
// empty interface. A named interface keeps decl-site identity — `I`
// and `interface{ M() }` are different types even when I declares
// exactly M(). Members whose signature cannot be recovered (facade
// requirements) compare by name alone, matching the name-only pass.
func (c sigComparer) ifaceTypEq(a, b *TypeDef) bool {
	if sigNamedIface(a) || sigNamedIface(b) {
		return c.namedTypEq(a, b)
	}
	ra, errA := c.res.IfaceReqs(a)
	rb, errB := c.res.IfaceReqs(b)
	if errA != nil || errB != nil {
		return TypIdentical(a, b)
	}
	if len(ra) != len(rb) {
		return false
	}
	sa, errA := c.res.IfaceSigs(a)
	sb, errB := c.res.IfaceSigs(b)
	if errA != nil || errB != nil {
		return TypIdentical(a, b)
	}
	for m := range ra {
		if !rb[m] {
			return false
		}
		fa, oka := sa[m]
		fb, okb := sb[m]
		if oka && okb && !c.sigIdentical(fa, fb) {
			return false
		}
	}
	return true
}

// sigNamedIface reports whether td is a NAMED interface for identity
// purposes — everything named except the predeclared `any`, which is
// defined as the alias of interface{} and so IS the anonymous empty
// interface (user code cannot reach here with its own `any`: a
// declared `type any ...` typedef carries a Spec, and an alias peels
// before this check).
func sigNamedIface(td *TypeDef) bool {
	if td.Name == "" {
		return false
	}
	return td.Name != "any" || td.Spec != nil || td.Pkg != nil || td.Anon != nil || len(td.MReqs) != 0 || len(td.IEmbeds) != 0
}

// namedTypEq is TypIdentical's named-typedef judgment with
// instantiation binds compared semantically: Pair[A] and Pair[int]
// name the same instantiation when A = int. Decl-site identity for
// func-local types is untouched — Spec equality still decides.
func (c sigComparer) namedTypEq(a, b *TypeDef) bool {
	ca, cb := *a, *b
	ca.Binds, cb.Binds = nil, nil
	if !TypIdentical(&ca, &cb) {
		return false
	}
	return c.bindsEqSig(a.Binds, b.Binds)
}

// bindsEqSig compares instantiation bindings like bindsEq but through
// sigTypEq — an argument spelled by alias identifies with its target.
func (c sigComparer) bindsEqSig(a, b map[string]Value) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !c.bindArgEqSig(av, bv) {
			return false
		}
	}
	return true
}

func (c sigComparer) bindArgEqSig(a, b Value) bool {
	at, aok := a.(*TypeDef)
	bt, bok := b.(*TypeDef)
	if aok != bok {
		return false
	}
	if !aok {
		return a == b
	}
	return c.sigTypEq(at, bt)
}

// sigFieldListEq compares two signature field lists pairwise — `a, b
// int` expands to two int params like typFieldSpellings does, and
// parameter names never decide identity.
func (c sigComparer) sigFieldListEq(fa *ast.FieldList, ca *TypeDef, fb *ast.FieldList, cb *TypeDef) bool {
	ea := sigFieldExprs(fa)
	eb := sigFieldExprs(fb)
	if len(ea) != len(eb) {
		return false
	}
	for i := range ea {
		if !c.sigExprEq(ea[i], ca, eb[i], cb) {
			return false
		}
	}
	return true
}

// sigFieldExprs expands a field list to one type expression per
// declared name — `a, b int` contributes int twice.
func sigFieldExprs(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, f.Type)
		}
	}
	return out
}

// sigExprEq compares two type expressions written in different
// declaration contexts: each resolves through the engine to a typedef
// and compares by sigTypEq; anything unresolvable falls back to the
// identity spelling. A variadic ellipsis is part of the signature —
// func(...T) is never func([]T).
func (c sigComparer) sigExprEq(ea ast.Expr, ca *TypeDef, eb ast.Expr, cb *TypeDef) bool {
	ae, aIs := ea.(*ast.Ellipsis)
	be, bIs := eb.(*ast.Ellipsis)
	if aIs != bIs {
		return false
	}
	if aIs {
		return c.sigExprEq(ae.Elt, ca, be.Elt, cb)
	}
	if ta, errA := c.res.ResolveType(ca, ea); errA == nil && ta != nil {
		if tb, errB := c.res.ResolveType(cb, eb); errB == nil && tb != nil {
			return c.sigTypEq(ta, tb)
		}
	}
	return TypSpelling(ea, ca) == TypSpelling(eb, cb)
}

// sigElemOf resolves the element typedef of a container typedef whose
// Anon cannot spell it — a synthesized `*T`, `[]T` or `chan T` that
// carries Elem, or one resolvable through the ElemOf hook.
func (c sigComparer) sigElemOf(td *TypeDef) *TypeDef {
	if td.Elem != nil {
		return td.Elem
	}
	if et, err := c.res.ElemOf(td); err == nil {
		return et
	}
	return nil
}

// sigElemEq compares element-carrying container typedefs — the
// fallback when one side cannot spell its shape as an AST.
func (c sigComparer) sigElemEq(a, b *TypeDef) bool {
	ea, eb := c.sigElemOf(a), c.sigElemOf(b)
	if ea == nil || eb == nil {
		return TypIdentical(a, b)
	}
	return c.sigTypEq(ea, eb)
}

// structTypEq compares two struct type ASTs field by field — names,
// types and tags in declaration order. `struct{ A }` and `struct{ T }`
// differ even when A is T's alias, since the embedded field keeps the
// written name; the field TYPE still compares semantically, so
// `struct{ x A }` and `struct{ x int }` are one type when A = int.
func (c sigComparer) structTypEq(sa *ast.StructType, ca *TypeDef, sb *ast.StructType, cb *TypeDef) bool {
	fa := sigStructFields(sa.Fields)
	fb := sigStructFields(sb.Fields)
	if len(fa) != len(fb) {
		return false
	}
	for i := range fa {
		if fa[i].name != fb[i].name {
			return false
		}
		if sigTagKey(fa[i].tag) != sigTagKey(fb[i].tag) {
			return false
		}
		if !c.sigExprEq(fa[i].typ, ca, fb[i].typ, cb) {
			return false
		}
	}
	return true
}

// sigStructField is one flattened struct field for identity compare.
type sigStructField struct {
	name string
	typ  ast.Expr
	tag  *ast.BasicLit
}

// sigStructFields expands a struct's field list like sigFieldExprs,
// keeping each field's name — embedded fields take the written type
// name like embedFieldName does.
func sigStructFields(fl *ast.FieldList) []sigStructField {
	if fl == nil {
		return nil
	}
	var out []sigStructField
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			out = append(out, sigStructField{name: sigEmbedName(f.Type), typ: f.Type, tag: f.Tag})
			continue
		}
		for _, n := range f.Names {
			out = append(out, sigStructField{name: n.Name, typ: f.Type, tag: f.Tag})
		}
	}
	return out
}

// sigEmbedName derives the field name of an embedded struct field —
// the written type name like embedBaseName: T for T or *T, T for
// pkg.T, G for G[int].
func sigEmbedName(x ast.Expr) string {
	switch t := x.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return sigEmbedName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return sigEmbedName(t.X)
	case *ast.IndexListExpr:
		return sigEmbedName(t.X)
	}
	return ""
}

// sigTagKey normalizes a field tag literal for identity compare, like
// structTagKey: absent and empty tags fold, and quote styles
// normalize.
func sigTagKey(tag *ast.BasicLit) string {
	if tag == nil {
		return ""
	}
	s, err := strconv.Unquote(tag.Value)
	if err != nil || s == "" {
		return ""
	}
	return strconv.Quote(s)
}

// sigLenEq compares array-length expressions for identity, mirroring
// typLenName: literals by value, named lengths by name — a named const
// is not folded, so [N]T and [3]T stay distinct here as they do in
// TypSpelling.
func sigLenEq(a, b ast.Expr) bool {
	return sigLenKey(a) == sigLenKey(b)
}

func sigLenKey(e ast.Expr) string {
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

// importClauseName resolves a file import's package clause name — the
// qualifier Go's type display uses regardless of the local import alias
// (`import o "x/odd"` where odd's clause is `package weird` displays
// `weird.T`). Returns "" when the import cannot be materialized (the
// caller falls back to the import-path basename).
func importClauseName(pkg *Package, file *syntax.File, alias string) string {
	if pkg == nil || file == nil {
		return ""
	}
	scopes := pkg.Scopes
	if scopes == nil {
		return ""
	}
	im := scopes[file][alias]
	if im == nil {
		return ""
	}
	p, err := im.Materialize()
	if err != nil || p == nil {
		return ""
	}
	return p.Name
}

// DisplayName renders td the way Go prints the type to the user —
// reflect.Type.String(), %T, and runtime panic text all share it: the
// qualifier is the declaring package's clause name, anonymous types
// spell canonically (`struct { f int }`, `interface { M() }`), and the
// predeclared aliases fold to their canonical types (byte→uint8).
// instArgsSpelling renders an instantiated generic's type arguments the
// way Go's Type.String does — `Pair[string,int]` keeps its binds in
// declaration order — and reports "" for unbound typedefs or binds that
// cannot spell every declared parameter.
func instArgsSpelling(td *TypeDef) string {
	if len(td.Binds) == 0 || (len(td.TParams) == 0 && len(td.OuterArgs) == 0) {
		return ""
	}
	outer, own := td.InstArgs()
	var b strings.Builder
	b.WriteByte('[')
	for i, a := range outer {
		otd, ok := a.Value.(*TypeDef)
		if !ok {
			return ""
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(instArgName(otd))
	}
	if len(outer) > 0 && len(own) > 0 {
		b.WriteByte(';')
	}
	for i, a := range own {
		btd, isTd := a.Value.(*TypeDef)
		if !a.Bound || !isTd {
			return ""
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(instArgName(btd))
	}
	b.WriteByte(']')
	return b.String()
}

// instArgName spells a typedef inside an instantiation's arg list — the
// only position gc decorates a function-local type's name with its
// `·gen` decl index (`main.T[main.L0·1]`); the head name of a spelled
// type stays bare (`main.T[...]`, `main.L0`).
func instArgName(td *TypeDef) string {
	ad := *td
	ad.inInstArgs = true
	return DisplayName(&ad)
}

// InstArg is one slot of an instantiated typedef's argument list — a
// resolved argument, or an unbound own-parameter slot (Bound=false) a
// caller may skip or treat as failure.
type InstArg struct {
	Value Value
	Bound bool
}

// InstArgs enumerates td's instantiation arguments in declaration order:
// outer carries the enclosing instantiation's resolved arguments
// (OuterArgs — always bound), own carries one slot per declared TParams
// entry resolved through Binds. Renderers of the `[outer;own]` suffix
// — instArgsSpelling for display, bindsKeyOf for canonical identity —
// walk the two runs and stringification stays per-callsite.
func (td *TypeDef) InstArgs() (outer, own []InstArg) {
	for _, ov := range td.OuterArgs {
		outer = append(outer, InstArg{Value: ov, Bound: true})
	}
	for _, p := range td.TParams {
		bv, ok := td.Binds[p]
		own = append(own, InstArg{Value: bv, Bound: ok})
	}
	return outer, own
}

func DisplayName(td *TypeDef) string {
	if td == nil {
		return "<nil>"
	}
	if td.Name != "" {
		name := td.Name
		if td.Pkg != nil && td.Pkg.Name != "" {
			// the qualifier is the package's declared NAME — prefer the
			// file's own package clause when available (a dir-loaded
			// package's Pkg.Name can be a synthesized import path).
			pkg := td.Pkg.Name
			if td.File != nil && td.File.AST != nil && td.File.AST.Name != nil {
				pkg = td.File.AST.Name.Name
			}
			if strings.HasPrefix(td.Name, td.Pkg.Path+".") {
				name = pkg + "." + td.Name[len(td.Pkg.Path)+1:]
			} else if i := strings.LastIndex(td.Name, "."); i >= 0 {
				name = pkg + td.Name[i:]
			} else {
				name = pkg + "." + td.Name
			}
		} else if i := strings.LastIndex(td.Name, "/"); i >= 0 {
			// a bound typedef's identity name is "pkgpath.Name" — display
			// keeps the last element like Go's package-name qualifier.
			name = td.Name[i+1:]
		} else if td.Pkg == nil && td.Spec == nil {
			// reflect spells the predeclared aliases by their canonical
			// types: `byte` prints `uint8`, `rune` prints `int32`.
			switch td.Name {
			case "byte":
				name = "uint8"
			case "rune":
				name = "int32"
			case "any":
				name = "interface {}"
			}
		}
		s := name + instArgsSpelling(td)
		if td.inInstArgs && td.Gen > 0 {
			s += "·" + strconv.Itoa(td.Gen)
		}
		return s
	}
	// A pointer synthesized around a concrete pointee typedef prefers the
	// typedef over its Anon spelling: the Anon selector names the decl
	// site (`*main.L0`) while the pointee clone carries the enclosing
	// instantiation's args (`*main.L0[int]`).
	if td.Kind == KindPointer && td.Elem != nil {
		elem := td.Elem
		if td.inInstArgs && !elem.inInstArgs {
			ec := *elem
			ec.inInstArgs = true
			elem = &ec
		}
		return "*" + DisplayName(elem)
	}
	anon := td.Anon
	if anon == nil && td.Spec != nil {
		anon = td.Spec.Type
	}
	if anon != nil {
		// synthesized composites keep an identity-path qualifier in
		// their Anon selector (*<dir>/x.T); Go's Type.String requalifies
		// it by the declaring package's clause name (*main.T). Borrow
		// the element's package context for display only — identity
		// spelling (TypSpelling/keyOf) keeps running on td itself.
		cd := *td
		for at := td; cd.Pkg == nil && at.Elem != nil; at = at.Elem {
			cd.Pkg = at.Elem.Pkg
			cd.File = at.Elem.File
		}
		return TypGoSpelling(anon, &cd)
	}
	if td.Elem != nil {
		elem := td.Elem
		if td.inInstArgs && !elem.inInstArgs {
			ec := *elem
			ec.inInstArgs = true
			elem = &ec
		}
		switch td.Kind {
		case KindPointer:
			return "*" + DisplayName(elem)
		case KindSlice:
			return "[]" + DisplayName(elem)
		case KindMap:
			return "map[?]" + DisplayName(elem)
		case KindChan:
			return "chan " + DisplayName(elem)
		case KindInterface:
			return "interface {}"
		}
	}
	return "<unnamed>"
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

// instHead spells an instantiation's head (`T` in `T[A]`): the head
// name never carries a `·gen` suffix or the enclosing args — a local
// head's `[outer;own]` bracket carries them instead — so it spells
// with a flagless, display-context-free copy of ctx.
func instHead(x ast.Expr, ctx *TypeDef) string {
	if ctx == nil {
		return TypGoSpelling(x, ctx)
	}
	hc := *ctx
	hc.inInstArgs = false
	hc.OuterSpell = nil
	return TypGoSpelling(x, &hc)
}

// outerSpellPrefix spells the enclosing instantiation's resolved args
// that lead a function-local head's bracket — gc spells `U[int]` inside
// F[int] as `main.U[int;int]·3`, the `int;` coming from the enclosing
// instantiation. Empty for non-local heads or context-free spellings.
func outerSpellPrefix(x ast.Expr, ctx *TypeDef) string {
	if ctx == nil || len(ctx.OuterSpell) == 0 {
		return ""
	}
	id, ok := ast.Unparen(x).(*ast.Ident)
	if !ok {
		return ""
	}
	btd := boundTypedef(ctx.Binds, id.Name)
	if btd == nil {
		btd = ctx.LocalTypes[id.Name]
	}
	if btd == nil || !btd.Local {
		return ""
	}
	var sb strings.Builder
	for i, a := range ctx.OuterSpell {
		otd, ok := a.(*TypeDef)
		if !ok {
			return ""
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(instArgName(otd))
	}
	sb.WriteByte(';')
	return sb.String()
}

// instArgGoSpelling spells an instantiation's type argument: inside an
// arg list every function-local type carries its `·gen` index.
func instArgGoSpelling(x ast.Expr, ctx *TypeDef) string {
	if ctx == nil {
		return TypGoSpelling(x, ctx)
	}
	ac := *ctx
	ac.inInstArgs = true
	return TypGoSpelling(x, &ac)
}

// instArgSuffix returns the `·gen` marker gc appends after the brackets
// of a function-local instantiation spelled inside an arg list —
// `main.T[main.U[int]·3]`. Empty outside arg context or for heads that
// are not a local decl (a `pkg.T[...]` selector has no gen).
func instArgSuffix(x ast.Expr, ctx *TypeDef) string {
	if ctx == nil || !ctx.inInstArgs {
		return ""
	}
	id, ok := ast.Unparen(x).(*ast.Ident)
	if !ok {
		return ""
	}
	btd := boundTypedef(ctx.Binds, id.Name)
	if btd == nil {
		btd = ctx.LocalTypes[id.Name]
	}
	if btd != nil && btd.Gen > 0 {
		return "·" + strconv.Itoa(btd.Gen)
	}
	return ""
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

// ArrayLenNodes lists the *ast.ArrayType nodes inside a type AST whose
// length is not a literal, in DFS order — `[]T`, `[3]T` and `[...]T`
// yield no nodes. This one walk defines the order the compiler emits
// length evals (OpFoldArrayLen) and the VM applies them: folding a
// node rewrites its Len to a BasicLit, so re-running the walk always
// finds the next un-folded length first.
func ArrayLenNodes(e ast.Expr) []*ast.ArrayType {
	var out []*ast.ArrayType
	var walk func(e ast.Expr)
	var walkList func(fl *ast.FieldList)
	walk = func(e ast.Expr) {
		switch t := e.(type) {
		case *ast.ArrayType:
			switch t.Len.(type) {
			case nil, *ast.BasicLit, *ast.Ellipsis:
			default:
				out = append(out, t)
			}
			walk(t.Elt)
		case *ast.MapType:
			walk(t.Key)
			walk(t.Value)
		case *ast.StarExpr:
			walk(t.X)
		case *ast.ParenExpr:
			walk(t.X)
		case *ast.ChanType:
			walk(t.Value)
		case *ast.FuncType:
			walkList(t.Params)
			walkList(t.Results)
		case *ast.StructType:
			walkList(t.Fields)
		case *ast.InterfaceType:
			walkList(t.Methods)
		case *ast.Ellipsis:
			walk(t.Elt)
		case *ast.IndexExpr:
			walk(t.Index)
		case *ast.IndexListExpr:
			for _, ix := range t.Indices {
				walk(ix)
			}
		}
	}
	walkList = func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, fd := range fl.List {
			walk(fd.Type)
		}
	}
	if e != nil {
		walk(e)
	}
	return out
}
