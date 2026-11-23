package minireflect

import (
	"fmt"
	"go/ast"
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/podhmo/minigo/runtime"
	"github.com/podhmo/minigo/syntax"
)

// RType is minigo's reflect.Type, for either a script type or a host
// type. RTypes are interned per engine, so two RTypes for the same type
// are the same pointer and compare equal with ==.
//
// Unlike reflect.Type, the methods return pointers (*[StructField],
// *[Method]).
type RType struct {
	e   *Env
	key string
	td  *runtime.TypeDef
	rt  reflect.Type

	// keyTd and array metadata carry the parts of a synthesized
	// map/array shape a TypeDef cannot express (map keys live in
	// td.Anon, which a facade-composed MapOf does not have).
	keyTd   *runtime.TypeDef
	isArray bool
	alen    int
}

// APIType spells the facade as reflect.Type — the public interface the
// facade stands in for, so a host slot typed reflect.Type (a method
// expression's receiver) accepts it.
func (t *RType) APIType() reflect.Type { return reflect.TypeFor[reflect.Type]() }

// StructField is minigo's reflect.StructField. Tag is a real
// reflect.StructTag.
type StructField struct {
	Name      string
	PkgPath   string
	Type      *RType
	Tag       reflect.StructTag
	Offset    uintptr
	Index     []int
	Anonymous bool
}

func (f *StructField) IsExported() bool { return f.PkgPath == "" }

// Method is minigo's reflect.Method. Func is the method expression, the
// value `T.M` or `(*T).M` evaluates to, taking the receiver as its first
// argument.
type Method struct {
	Name    string
	PkgPath string
	Type    *RType
	Index   int
	Func    *RValue
}

// rtypeOf interns a script typedef.
func (e *Env) rtypeOf(td *runtime.TypeDef) *RType {
	// an alias IS its target: the Type carries the target typedef so
	// Name, methods and Implements see the aliased declaration
	// (kin-openapi's `type AdditionalProperties = BoolSchema`).
	td = e.peelAlias(td)
	// a facade typedef also carries the real gc type it stands in
	// for — field/kind ops answer the public surface (reflect.Value's
	// abi fields), while methodRT maps it back to the internal type
	// whose methods dispatch on facade receivers.
	return e.intern(e.keyOf(td), td, e.facadeRT[td])
}

// peelAlias follows `type A = T` chains to the target typedef.
func (e *Env) peelAlias(td *runtime.TypeDef) *runtime.TypeDef {
	for i := 0; td != nil && td.Kind == runtime.KindAlias && e.h.AliasOf != nil && i < 100; i++ {
		t, err := e.h.AliasOf(td)
		if err != nil || t == nil || t == td {
			break
		}
		td = t
	}
	return td
}

// hostTypeOf interns a host reflect.Type. A host type that stands in
// for one of the facade's public types (*RValue for reflect.Value)
// maps back to that typedef, so signatures and field types spelled
// with it report the public type — `func() reflect.Value`, not
// `func() *minireflect.RValue`.
func (e *Env) hostTypeOf(rt reflect.Type) *RType {
	if rt != nil {
		rt = e.publicRT(rt)
		if td, ok := e.facadeTyp[rt]; ok {
			return e.rtypeOf(td)
		}
		if e.h.TypeAlias != nil {
			rt = e.h.TypeAlias(rt)
		}
	}
	return e.intern(hostTypeKey(rt), nil, rt)
}

// publicRT rewrites the facade's own host types inside a host type to
// the real gc types they stand in for, so a signature spells the
// public API — `func(*reflect.MapIter) reflect.Value`, not
// `func(*minireflect.MapIter) *minireflect.RValue`. Composites are
// rebuilt when an element changed.
func (e *Env) publicRT(rt reflect.Type) reflect.Type {
	return e.publicRTSeen(rt, map[reflect.Type]bool{})
}

// publicRTSeen is publicRT with a guard against host types that recur
// through their own shape — `type Nodes []Nodes` has no facade
// substitution but walks its element forever without one.
func (e *Env) publicRTSeen(rt reflect.Type, seen map[reflect.Type]bool) reflect.Type {
	if pub, ok := e.facadePub[rt]; ok {
		return pub
	}
	if seen[rt] {
		return rt
	}
	// in-progress marking only: a completed rewrite stays reusable,
	// so the same composite in two positions rewrites both.
	seen[rt] = true
	defer delete(seen, rt)
	switch rt.Kind() {
	case reflect.Pointer:
		if el := e.publicRTSeen(rt.Elem(), seen); el != rt.Elem() {
			return reflect.PointerTo(el)
		}
	case reflect.Slice:
		if el := e.publicRTSeen(rt.Elem(), seen); el != rt.Elem() {
			return reflect.SliceOf(el)
		}
	case reflect.Array:
		if el := e.publicRTSeen(rt.Elem(), seen); el != rt.Elem() {
			return reflect.ArrayOf(rt.Len(), el)
		}
	case reflect.Map:
		k, el := e.publicRTSeen(rt.Key(), seen), e.publicRTSeen(rt.Elem(), seen)
		if k != rt.Key() || el != rt.Elem() {
			return reflect.MapOf(k, el)
		}
	case reflect.Chan:
		if el := e.publicRTSeen(rt.Elem(), seen); el != rt.Elem() {
			return reflect.ChanOf(rt.ChanDir(), el)
		}
	case reflect.Func:
		changed := false
		ins := make([]reflect.Type, rt.NumIn())
		for i := range ins {
			ins[i] = e.publicRTSeen(rt.In(i), seen)
			changed = changed || ins[i] != rt.In(i)
		}
		outs := make([]reflect.Type, rt.NumOut())
		for i := range outs {
			outs[i] = e.publicRTSeen(rt.Out(i), seen)
			changed = changed || outs[i] != rt.Out(i)
		}
		if changed {
			return reflect.FuncOf(ins, outs, rt.IsVariadic())
		}
	}
	return rt
}

// hostRTOf derives the host reflect.Type a typedef stands for, when
// the typedef backs a bound host type. Pointer typedefs wrap their
// element's bound type — a bound struct's rep already mints a pointer
// (*bytes.Buffer), so the rep is unwrapped to the named type first and
// the typedef's own pointer depth re-applied.
func (e *Env) hostRTOf(td *runtime.TypeDef) reflect.Type {
	depth := 0
	seen := map[*runtime.TypeDef]bool{}
	for td != nil && td.Kind == runtime.KindPointer {
		if seen[td] {
			// a recursive pointer type (`type P *P`) peels forever —
			// it has no host type to stand for anyway.
			return nil
		}
		seen[td] = true
		et := td.Elem
		if et == nil {
			et = e.elemOf(td)
		}
		if et == nil {
			return nil
		}
		td = et
		depth++
	}
	if td == nil {
		return nil
	}
	var rt reflect.Type
	switch {
	case td.HostNew != nil:
		rt = td.HostType()
	case td.HostScalar != nil:
		rt = reflect.TypeOf(td.HostScalar)
	}
	if rt == nil {
		return nil
	}
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	for ; depth > 0; depth-- {
		rt = reflect.PointerTo(rt)
	}
	return rt
}

func (e *Env) intern(key string, td *runtime.TypeDef, rt reflect.Type) *RType {
	return e.internT(key, &RType{td: td, rt: rt})
}

// internT interns a fully built RType under a precomputed key.
func (e *Env) internT(key string, t *RType) *RType {
	if got, ok := e.types.Load(key); ok {
		return got.(*RType)
	}
	t.e, t.key = e, key
	got, _ := e.types.LoadOrStore(key, t)
	return got.(*RType)
}

// keyOf computes the canonical interning key for a typedef: the
// package-qualified name for named types (matching the host side's
// "pkgpath.Name"), a structural spelling for anonymous shapes, and a
// pointer identity for synthesized shapes that carry no spelling.
func (e *Env) keyOf(td *runtime.TypeDef) string {
	if td == nil {
		return "<nil type>"
	}
	if td.Local {
		// a function-local declaration is its own type — `type X int`
		// in two different functions are distinct even though they
		// share a name and package. The Spec pointer is the declaration
		// object, so it carries the identity; type args still separate
		// instantiations of a local generic.
		return fmt.Sprintf("td:%p%s", td.Spec, e.bindsKeyOf(td))
	}
	if td.Kind == runtime.KindAlias && e.h.AliasOf != nil {
		// an alias shares its target's identity: `type A = int` IS int.
		if t, err := e.h.AliasOf(td); err == nil && t != nil && t != td {
			return e.keyOf(t)
		}
	}
	if td.Name != "" {
		name := td.Name
		if td.Pkg != nil && td.Pkg.Path != "" && !strings.HasPrefix(name, td.Pkg.Path+".") {
			name = td.Pkg.Path + "." + name
		}
		// predeclared aliases fold to their canonical type: byte IS
		// uint8, rune IS int32 — but only when the name is the builtin
		// itself (a user `type byte int` keeps its own identity).
		if td.Spec == nil && td.Pkg == nil {
			switch name {
			case "byte":
				name = "uint8"
			case "rune":
				name = "int32"
			}
		}
		return name + e.bindsKeyOf(td)
	}
	if td.Anon != nil {
		if s := runtime.TypSpelling(td.Anon, td); s != "" {
			// a host-derived element spells the composite by the host
			// type's qualified name — matching hostTypeKey's
			// "anon:"+rt.String() form — where the bare Anon ident
			// would collide across packages ([]*inspect.File vs
			// []*ast.Field both spell []*Field).
			if hs := e.hostElemKey(td); hs != "" {
				return "anon:" + hs
			}
			return "anon:" + s
		}
	}
	if td.Elem != nil {
		switch td.Kind {
		case runtime.KindPointer:
			return "anon:*" + e.spellOf(td.Elem)
		case runtime.KindSlice:
			return "anon:[]" + e.spellOf(td.Elem)
		case runtime.KindChan:
			return "anon:chan " + e.spellOf(td.Elem)
		case runtime.KindMap:
			// map keys ride on td.Anon (or keyTd on RType) — the
			// spelling branch above catches the anon case.
		}
	}
	return fmt.Sprintf("td:%p", td)
}

// bindsKeyOf renders a typedef's instantiation arguments as the
// "[arg,...]" suffix of its canonical name — `S[int]` and `S[string]`
// share a declared name but are different types. A function-local type
// declared inside a generic function is implicitly parameterized by the
// enclosing type args too: they key first, separated from the type's own
// args by ';' like instArgsSpelling's `[outer;own]` rendering.
func (e *Env) bindsKeyOf(td *runtime.TypeDef) string {
	if len(td.OuterArgs) == 0 && (len(td.TParams) == 0 || len(td.Binds) == 0) {
		return ""
	}
	outer, own := td.InstArgs()
	var sb strings.Builder
	sb.WriteString("[")
	for i, a := range outer {
		otd, _ := a.Value.(*runtime.TypeDef)
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(e.keyOf(otd))
	}
	wrote := len(outer) > 0
	if wrote {
		sb.WriteString(";")
	}
	for _, a := range own {
		if !a.Bound {
			continue
		}
		atd, _ := a.Value.(*runtime.TypeDef)
		if wrote {
			sb.WriteString(",")
		}
		sb.WriteString(e.keyOf(atd))
		wrote = true
	}
	if !wrote {
		return ""
	}
	sb.WriteString("]")
	return sb.String()
}

// hostElemKey spells a typedef by the host type identity it or its
// element chain carries — the same words reflect's rt.String() uses —
// or "" when no host identity is recorded, or when the Anon element is
// already package-qualified (a bound facade's stamped
// []reflect.Value keys by its own spelling, not the internal
// *minireflect.RValue element). An array's length and a map's key come
// from the Anon spelling — a named host key has no typedef slot, so a
// map of named keys still keys by the anon words.
func (e *Env) hostElemKey(td *runtime.TypeDef) string {
	if td == nil {
		return ""
	}
	if td.HostIdent != nil {
		return td.HostIdent.String()
	}
	if td.Elem != nil {
		inner := e.hostElemKey(td.Elem)
		if inner == "" {
			return ""
		}
		switch td.Kind {
		case runtime.KindPointer:
			return "*" + inner
		case runtime.KindSlice:
			if at, isArr := td.Anon.(*ast.ArrayType); isArr {
				if runtime.AnonEltQualified(at.Elt) {
					return ""
				}
				if at.Len != nil {
					return "[" + runtime.TypLenName(at.Len) + "]" + inner
				}
			}
			return "[]" + inner
		case runtime.KindMap:
			if mt, isMap := td.Anon.(*ast.MapType); isMap {
				if runtime.AnonEltQualified(mt.Value) {
					return ""
				}
				return "map[" + runtime.TypGoSpelling(mt.Key, td) + "]" + inner
			}
		case runtime.KindChan:
			pre := "chan "
			if ct, isChan := td.Anon.(*ast.ChanType); isChan {
				if runtime.AnonEltQualified(ct.Value) {
					return ""
				}
				switch ct.Dir {
				case ast.RECV:
					pre = "<-chan "
				case ast.SEND:
					pre = "chan<- "
				}
			}
			return pre + inner
		}
	}
	return ""
}

// spellOf renders a typedef the way it would appear inside a composite
// type spelling — matching TypSpelling's canonicalization (predeclared
// aliases fold, named types qualify by package).
func (e *Env) spellOf(td *runtime.TypeDef) string {
	if td == nil {
		return "interface{}"
	}
	return runtime.TypSpelling(e.exprOf(td), td)
}

// exprOf renders a typedef back as a type-expression AST so a
// synthesized composite keeps a canonical Anon spelling: named types
// spell by name (package-qualified by selector, instantiated by index),
// anonymous typedefs reuse their declared expression, and Elem-only
// typedefs rebuild structurally.
func (e *Env) exprOf(td *runtime.TypeDef) ast.Expr {
	if td == nil {
		return ast.NewIdent("interface{}")
	}
	if td.Name != "" {
		name := td.Name
		var x ast.Expr
		if i := strings.LastIndex(name, "."); i >= 0 {
			// bound typedefs name themselves "pkgpath.Name" — the
			// qualifier doubles as the package reference.
			x = &ast.SelectorExpr{X: ast.NewIdent(name[:i]), Sel: ast.NewIdent(name[i+1:])}
		} else {
			x = ast.NewIdent(name)
		}
		if td.Pkg != nil && td.Pkg.Path != "" {
			// qualify by package PATH, not Name: the own package of a
			// script is `main` under `go run` but the canonical type
			// key spells the module path (ucreflectclone.AppConfig).
			// The selector carries only the LOCAL name — the full
			// "pkg.T" would spell "pkg.pkg.T" on display.
			local := name
			if j := strings.LastIndex(name, "."); j >= 0 {
				local = name[j+1:]
			}
			x = &ast.SelectorExpr{X: ast.NewIdent(td.Pkg.Path), Sel: ast.NewIdent(local)}
		}
		if len(td.TParams) > 0 && len(td.Binds) > 0 {
			var args []ast.Expr
			for _, tp := range td.TParams {
				if bv, ok := td.Binds[tp]; ok {
					if atd, ok := bv.(*runtime.TypeDef); ok {
						args = append(args, e.exprOf(atd))
					}
				}
			}
			switch len(args) {
			case 0:
			case 1:
				x = &ast.IndexExpr{X: x, Index: args[0]}
			default:
				x = &ast.IndexListExpr{X: x, Indices: args}
			}
		}
		return x
	}
	if td.Anon != nil {
		return td.Anon
	}
	if td.Spec != nil {
		return td.Spec.Type
	}
	if td.Elem != nil {
		switch td.Kind {
		case runtime.KindPointer:
			return &ast.StarExpr{X: e.exprOf(td.Elem)}
		case runtime.KindSlice:
			return &ast.ArrayType{Elt: e.exprOf(td.Elem)}
		case runtime.KindChan:
			return &ast.ChanType{Dir: ast.RECV | ast.SEND, Value: e.exprOf(td.Elem)}
		}
	}
	return ast.NewIdent("interface{}")
}

// hostTypeKey mirrors keyOf for host types: named types key by
// pkgpath.name (which script tds produce too), unnamed shapes by
// reflect's structural spelling.
func hostTypeKey(rt reflect.Type) string {
	if rt == nil {
		return "<nil type>"
	}
	if rt.Name() != "" {
		if pp := rt.PkgPath(); pp != "" {
			return pp + "." + rt.Name()
		}
		return rt.Name()
	}
	return "anon:" + rt.String()
}

// elemOf resolves a td's element/pointee typedef.
func (e *Env) elemOf(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	if td.Elem != nil {
		return td.Elem
	}
	if e.h.ElemOf != nil {
		if et, err := e.h.ElemOf(td); err == nil && et != nil {
			return et
		}
	}
	if e.h.ResolveType != nil && td.Anon != nil {
		var x ast.Expr
		switch a := td.Anon.(type) {
		case *ast.StarExpr:
			x = a.X
		case *ast.ArrayType:
			x = a.Elt
		case *ast.MapType:
			x = a.Value
		case *ast.ChanType:
			x = a.Value
		}
		if x != nil {
			if et, err := e.resolveExpr(td, x); err == nil {
				return et
			}
		}
	}
	return nil
}

// resolveExpr resolves a full type expression to a typedef — unlike the
// embed-spec resolver it keeps StarExpr as a pointer typedef, so
// `func(*T)` params and `[]*T`/`map[K]*T` elements retain the star.
func (e *Env) resolveExpr(from *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	if st, ok := x.(*ast.StarExpr); ok {
		etd, err := e.resolveExpr(from, st.X)
		if err != nil {
			return nil, err
		}
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: etd,
			Anon: st, Pkg: from.Pkg, File: from.File, Binds: from.Binds, LocalTypes: from.LocalTypes}, nil
	}
	if e.h.ResolveType == nil {
		return nil, fmt.Errorf("minireflect: type resolution needs ResolveType hook")
	}
	return e.h.ResolveType(from, x)
}

// keyTdOf resolves a map td's key type.
func (e *Env) keyTdOf(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	if e.h.ResolveType != nil {
		if mt, ok := td.Anon.(*ast.MapType); ok {
			if kt, err := e.resolveExpr(td, mt.Key); err == nil {
				return kt
			}
		}
	}
	return nil
}

// fieldTypes wraps the hook, tolerating its absence.
func (e *Env) fieldTypes(td *runtime.TypeDef) []*runtime.TypeDef {
	if e.h.FieldTypes == nil || td == nil {
		return nil
	}
	fts, err := e.h.FieldTypes(td)
	if err != nil {
		return nil
	}
	// alias-typed fields view their target: Addr() of a field declared
	// `AP AdditionalProperties` is a *BoolSchema with its methods.
	for _, ft := range fts {
		if ft != nil && ft.Kind == runtime.KindAlias {
			out := make([]*runtime.TypeDef, len(fts))
			for j, t := range fts {
				out[j] = e.peelAlias(t)
			}
			return out
		}
	}
	return fts
}

// methodSet wraps the hook: the typedef's method functions, declared
// plus promoted under Go's receiver rule, unexported included.
func (e *Env) methodSet(td *runtime.TypeDef) map[string]*runtime.Function {
	if e.h.MethodSet == nil || td == nil {
		return nil
	}
	m, err := e.h.MethodSet(td)
	if err != nil {
		return nil
	}
	return m
}

// exportedMethodNames sorts the exported names of a method set —
// reflect's NumMethod/Method expose exported methods only.
func exportedMethodNames(set map[string]*runtime.Function) []string {
	var out []string
	for name := range set {
		if ast.IsExported(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// methodType builds the RType Go reports as Method.Type — the declared
// signature with the receiver prepended as the first parameter, so
// S{}.F on type S reads func(main.S).
func (e *Env) methodType(t *RType, m *runtime.Function) *RType {
	if m == nil || m.Decl == nil {
		return nil
	}
	ft := m.Decl.Type
	params := &ast.FieldList{}
	if t != nil && t.td != nil {
		params.List = append(params.List, &ast.Field{Type: e.exprOf(t.td)})
	}
	if ft.Params != nil {
		params.List = append(params.List, ft.Params.List...)
	}
	td := &runtime.TypeDef{
		Kind: runtime.KindFunc,
		Anon: &ast.FuncType{Params: params, Results: ft.Results},
		Pkg:  m.Pkg, File: m.File, Binds: m.Binds,
	}
	return e.rtypeOf(td)
}

// basicKinds maps builtin type names to reflect kinds.
var basicKinds = map[string]reflect.Kind{
	"bool": reflect.Bool,
	"int":  reflect.Int, "int8": reflect.Int8, "int16": reflect.Int16,
	"int32": reflect.Int32, "int64": reflect.Int64, "rune": reflect.Int32,
	"uint": reflect.Uint, "uint8": reflect.Uint8, "uint16": reflect.Uint16,
	"uint32": reflect.Uint32, "uint64": reflect.Uint64, "byte": reflect.Uint8,
	"uintptr": reflect.Uintptr,
	"float32": reflect.Float32, "float64": reflect.Float64,
	"complex64": reflect.Complex64, "complex128": reflect.Complex128,
	"string": reflect.String, "any": reflect.Interface, "error": reflect.Interface,
}

// namedBasicKinds reports kinds for bound named basic typedefs whose
// Anon is absent (host-bound packages declare only a name).
var namedBasicKinds = map[string]reflect.Kind{
	"time.Duration":     reflect.Int64,
	"time.Month":        reflect.Int,
	"time.Weekday":      reflect.Int,
	"json.Number":       reflect.String,
	"reflect.StructTag": reflect.String,
	"reflect.Kind":      reflect.Uint,
	"reflect.ChanDir":   reflect.Int,
	"unsafe.Pointer":    reflect.UnsafePointer,
}

// kindOfTd maps a typedef to its reflect.Kind.
func (e *Env) kindOfTd(td *runtime.TypeDef) reflect.Kind {
	if td == nil {
		return reflect.Invalid
	}
	if td.Name != "" {
		if k, ok := basicKinds[td.Name]; ok {
			return k
		}
		if k, ok := namedBasicKinds[td.Name]; ok {
			return k
		}
	}
	switch td.Kind {
	case runtime.KindStruct:
		return reflect.Struct
	case runtime.KindSlice:
		if at, ok := td.Anon.(*ast.ArrayType); ok && at.Len != nil {
			return reflect.Array
		}
		return reflect.Slice
	case runtime.KindMap:
		return reflect.Map
	case runtime.KindFunc:
		return reflect.Func
	case runtime.KindInterface:
		return reflect.Interface
	case runtime.KindChan:
		return reflect.Chan
	case runtime.KindPointer:
		return reflect.Ptr
	case runtime.KindAlias, runtime.KindNamedBasic:
		return e.kindOfAnon(td)
	}
	if td.Anon != nil {
		return e.kindOfAnon(td)
	}
	return reflect.Invalid
}

// kindOfAnon maps an anonymous underlying expr to a kind.
func (e *Env) kindOfAnon(td *runtime.TypeDef) reflect.Kind {
	switch a := td.Anon.(type) {
	case *ast.Ident:
		if k, ok := basicKinds[a.Name]; ok {
			return k
		}
	case *ast.StarExpr:
		return reflect.Ptr
	case *ast.ArrayType:
		if a.Len != nil {
			return reflect.Array
		}
		return reflect.Slice
	case *ast.MapType:
		return reflect.Map
	case *ast.FuncType:
		return reflect.Func
	case *ast.InterfaceType:
		return reflect.Interface
	case *ast.ChanType:
		return reflect.Chan
	case *ast.StructType:
		return reflect.Struct
	}
	if e.h.Underlying != nil {
		if u, err := e.h.Underlying(td); err == nil && u != nil && u != td {
			return e.kindOfTd(u)
		}
	}
	return reflect.Invalid
}

// structTd resolves the typedef carrying a struct's field list: a type
// defined over another struct (`type TBis T`) has no Fields of its own,
// so its underlying typedef supplies them.
func (e *Env) structTd(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil || td.Kind == runtime.KindStruct || e.h.Underlying == nil {
		return td
	}
	if u, err := e.h.Underlying(td); err == nil && u != nil && u != td && u.Kind == runtime.KindStruct {
		return u
	}
	return td
}

// ---- reflect.Type methods ----

func (t *RType) Kind() reflect.Kind {
	if t.rt != nil {
		return t.rt.Kind()
	}
	if t.isArray {
		return reflect.Array
	}
	return t.e.kindOfTd(t.td)
}

func (t *RType) Name() string {
	if t.rt != nil {
		return t.rt.Name()
	}
	if t.td == nil || t.td.Name == "" {
		return ""
	}
	name := t.td.Name
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	// the predeclared aliases are identity, not names — byte reports
	// uint8 and rune int32 like Go's reflect (a user-declared
	// `type byte int` keeps its own name; only the builtin folds).
	if t.td.Spec == nil && t.td.Pkg == nil {
		switch name {
		case "byte":
			return "uint8"
		case "rune":
			return "int32"
		case "any":
			// `any` is the empty interface — an unnamed type.
			return ""
		}
	}
	return name
}

func (t *RType) PkgPath() string {
	if t.rt != nil {
		return t.rt.PkgPath()
	}
	if t.td == nil {
		return ""
	}
	if t.td.Name == "" {
		// unnamed types carry no package path
		return ""
	}
	if t.td.Pkg != nil {
		// a type in package main reports "main", not the directory or
		// synthesized import path its package object was loaded under.
		if t.td.File != nil && t.td.File.AST != nil && t.td.File.AST.Name != nil {
			if t.td.File.AST.Name.Name == "main" {
				return "main"
			}
		}
		return t.td.Pkg.Path
	}
	if i := strings.LastIndex(t.td.Name, "."); i >= 0 {
		return t.td.Name[:i]
	}
	return ""
}

func (t *RType) String() string {
	if t.rt != nil {
		return t.rt.String()
	}
	return runtime.DisplayName(t.td)
}

func (t *RType) Elem() *RType {
	if t.rt != nil {
		return t.e.hostTypeOf(t.rt.Elem())
	}
	switch t.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Chan, reflect.Array:
		return t.e.rtypeOf(t.e.elemOf(t.td))
	}
	trap("Elem of invalid type %s", t.String())
	return nil
}

func (t *RType) Key() *RType {
	if t.rt != nil {
		return t.e.hostTypeOf(t.rt.Key())
	}
	if t.keyTd != nil {
		return t.e.rtypeOf(t.keyTd)
	}
	if t.Kind() != reflect.Map {
		trap("Key of non-map type %s", t.String())
	}
	return t.e.rtypeOf(t.e.keyTdOf(t.td))
}

func (t *RType) Len() int {
	if t.rt != nil {
		return t.rt.Len()
	}
	if t.isArray {
		return t.alen
	}
	if at, ok := t.td.Anon.(*ast.ArrayType); ok && at.Len != nil {
		if n, ok := runtime.FoldedArrayLen(t.td.Pkg, at); ok {
			return int(n)
		}
	}
	// a named type declared over an array gets its length through the
	// underlying typedef — `type B [2]int` carries the ident "A", not
	// an ArrayType, in td.Anon.
	if t.e.h.Underlying != nil && t.td != nil {
		if u, err := t.e.h.Underlying(t.td); err == nil && u != nil && u != t.td {
			return t.e.rtypeOf(u).Len()
		}
	}
	trap("Len of non-array type %s", t.String())
	return 0
}

func (t *RType) NumField() int {
	if t.rt != nil {
		return t.rt.NumField()
	}
	if t.Kind() != reflect.Struct {
		trap("NumField of non-struct type %s", t.String())
	}
	return len(t.e.structTd(t.td).Fields)
}

func (t *RType) Field(i int) *StructField {
	if t.rt != nil {
		f := t.rt.Field(i)
		return &StructField{
			Name:      f.Name,
			PkgPath:   f.PkgPath,
			Type:      t.e.hostTypeOf(f.Type),
			Tag:       f.Tag,
			Offset:    f.Offset,
			Index:     f.Index,
			Anonymous: f.Anonymous,
		}
	}
	if t.Kind() != reflect.Struct {
		trap("Field of non-struct type %s", t.String())
	}
	st := t.e.structTd(t.td)
	if i < 0 || i >= len(st.Fields) {
		panic(&runtime.Panic{Value: "reflect: Field index out of bounds"})
	}
	fts := t.e.fieldTypes(st)
	var ft *RType
	if i < len(fts) {
		ft = t.e.rtypeOf(fts[i])
	}
	embedded := false
	for _, ei := range st.EmbedIdx {
		if ei == i {
			embedded = true
			break
		}
	}
	name := st.Fields[i]
	sf := &StructField{
		Name:      name,
		Type:      ft,
		Offset:    fieldOffset(t, fts, i),
		Index:     []int{i},
		Anonymous: embedded,
	}
	if name != "" && !unicode.IsUpper(rune(name[0])) {
		// unexported field — embedded or not — carries the declaring
		// package path, like Go's reflect.
		sf.PkgPath = t.PkgPath()
	}
	if st.FTags != nil {
		sf.Tag = reflect.StructTag(st.FTags[name])
	}
	return sf
}

func (t *RType) FieldByIndex(idx []int) *StructField {
	if t.rt != nil {
		f := t.rt.FieldByIndex(idx)
		return &StructField{Name: f.Name, PkgPath: f.PkgPath,
			Type: t.e.hostTypeOf(f.Type), Tag: f.Tag, Offset: f.Offset,
			Index: f.Index, Anonymous: f.Anonymous}
	}
	cur := t
	var f *StructField
	for depth, i := range idx {
		// embedded traversal derefs a ptr-to-struct field between
		// steps — [ptrField, inner] walks the pointee like Go.
		if depth > 0 && cur.Kind() == reflect.Ptr {
			et := cur.Elem()
			if et != nil && et.Kind() == reflect.Struct {
				cur = et
			}
		}
		// Go checks each level: descending into a non-struct panics
		// with the level's type, not the top type — and the deeper
		// levels fail inside Field, so the wording changes.
		if cur.Kind() != reflect.Struct {
			if depth == 0 {
				trap("FieldByIndex of non-struct type %s", cur.String())
			}
			trap("Field of non-struct type %s", cur.String())
		}
		f = cur.Field(i)
		if f.Type != nil {
			cur = f.Type
		}
	}
	return f
}

func (t *RType) FieldByName(name string) (*StructField, bool) {
	if t.rt != nil {
		f, ok := t.rt.FieldByName(name)
		if !ok {
			return nil, false
		}
		return &StructField{Name: f.Name, PkgPath: f.PkgPath,
			Type: t.e.hostTypeOf(f.Type), Tag: f.Tag, Offset: f.Offset,
			Index: f.Index, Anonymous: f.Anonymous}, true
	}
	if t.Kind() != reflect.Struct {
		trap("FieldByName of non-struct type %s", t.String())
	}
	st := t.e.structTd(t.td)
	for i, fn := range st.Fields {
		if fn == name {
			return t.Field(i), true
		}
	}
	fts := t.e.fieldTypes(st)
	for _, ei := range st.EmbedIdx {
		if ei < len(fts) && fts[ei] != nil {
			etd := fts[ei]
			if etd.Kind == runtime.KindPointer {
				// promotion through an embedded pointer field (*Inner)
				if et := t.e.elemOf(etd); et != nil {
					etd = et
				}
			}
			if sub := t.e.rtypeOf(etd); sub.Kind() == reflect.Struct {
				if f, ok := sub.FieldByName(name); ok {
					f.Index = append([]int{ei}, f.Index...)
					// Go reports the field's LOCAL offset inside the
					// declaring struct — same as FieldByIndex — not
					// the top-level position the embed chain implies.
					return f, true
				}
			}
		}
	}
	return nil, false
}

func (t *RType) FieldByNameFunc(match func(string) bool) (*StructField, bool) {
	// The traversal is Go's own: breadth first search, one depth level
	// at a time, where two matches at the same depth annihilate each
	// other and a struct type reachable through multiple embedded paths
	// annihilates its own matches.
	if t.rt != nil {
		f, ok := t.rt.FieldByNameFunc(match)
		if !ok {
			return nil, false
		}
		return &StructField{Name: f.Name, PkgPath: f.PkgPath,
			Type: t.e.hostTypeOf(f.Type), Tag: f.Tag, Offset: f.Offset,
			Index: f.Index, Anonymous: f.Anonymous}, true
	}
	if t.Kind() != reflect.Struct {
		trap("FieldByNameFunc of non-struct type %s", t.String())
	}
	type scan struct {
		td    *runtime.TypeDef
		index []int
	}
	embedded := func(td *runtime.TypeDef, i int) bool {
		for _, ei := range td.EmbedIdx {
			if ei == i {
				return true
			}
		}
		return false
	}
	next := []scan{{td: t.e.structTd(t.td)}}
	var nextCount map[*runtime.TypeDef]int
	visited := map[*runtime.TypeDef]bool{}
	var result *StructField
	ok := false
	for len(next) > 0 {
		var current []scan
		current, next = next, current
		count := nextCount
		nextCount = nil
		for _, sc := range current {
			st := t.e.structTd(sc.td)
			if visited[st] {
				continue
			}
			visited[st] = true
			fts := t.e.fieldTypes(st)
			for i, fname := range st.Fields {
				var ntyp *runtime.TypeDef
				if embedded(st, i) && i < len(fts) {
					ntyp = fts[i]
					if ntyp != nil && ntyp.Kind == runtime.KindPointer {
						ntyp = t.e.elemOf(ntyp)
					}
				}
				if match(fname) {
					// a second match at this depth annihilates both —
					// Go reports no field at all.
					if count[st] > 1 || ok {
						return nil, false
					}
					f := t.e.rtypeOf(st).Field(i)
					f.Index = append(append([]int{}, sc.index...), i)
					result = f
					ok = true
					continue
				}
				// embedded struct fields queue for the next depth —
				// only while no match exists at this one, and a type
				// already queued marks itself multiply-reachable.
				if ok || ntyp == nil || ntyp.Kind != runtime.KindStruct {
					continue
				}
				if nextCount[ntyp] > 0 {
					nextCount[ntyp] = 2
					continue
				}
				if nextCount == nil {
					nextCount = map[*runtime.TypeDef]int{}
				}
				nextCount[ntyp] = 1
				if count[st] > 1 {
					nextCount[ntyp] = 2
				}
				next = append(next, scan{td: ntyp,
					index: append(append([]int{}, sc.index...), i)})
			}
		}
		if ok {
			break
		}
	}
	return result, ok
}

// methodRT returns the reflect.Type whose method set this Type
// reports: the host type itself, or the host type a bound typedef
// stands for (time.Weekday's methods live on the host type, not in
// the script method set).
func (t *RType) methodRT() reflect.Type {
	if t.rt != nil {
		// the real gc type's own method set — enumeration matches gc
		// exactly (reflect.Value reports 79, reflect.MapIter 0)
		return t.rt
	}
	if t.td != nil {
		return t.e.hostRTOf(t.td)
	}
	return nil
}

// methodSigType rewrites a concrete method's signature to its public
// spelling: facade types map to the real gc types, and the receiver —
// In(0) — keeps the enumerated type's pointer shape (gc spells
// TypeFor[*T].Method as func(*T, ...) even for value-receiver
// methods).
func (e *Env) methodSigType(recv, mt reflect.Type) reflect.Type {
	pub := e.publicRT(mt)
	if mt == nil || recv.Kind() != reflect.Pointer || mt.NumIn() == 0 || mt.In(0) != recv {
		return pub
	}
	pr := e.publicRT(recv)
	if pr.Kind() != reflect.Pointer {
		pr = reflect.PointerTo(pr)
	}
	ins := make([]reflect.Type, mt.NumIn())
	ins[0] = pr
	for i := 1; i < mt.NumIn(); i++ {
		ins[i] = pub.In(i)
	}
	outs := make([]reflect.Type, mt.NumOut())
	for i := range outs {
		outs[i] = pub.Out(i)
	}
	return reflect.FuncOf(ins, outs, mt.IsVariadic())
}

// facadeInternalMethods names exported methods of the facade's own
// types (*RValue, *RType) that are not part of gc's public reflect
// surface — the other facades (MapIter, StructField) expose exactly
// the public names already.
var facadeInternalMethods = map[string]bool{
	"APIType": true, // the facade's api-type marker
	"Payload": true, // RValue's inner-value reader
}

// hostMethodNames lists rt's exported method names in reflect order,
// minus the internal facade methods a minireflect type would report
// that gc's public reflect API does not have.
func hostMethodNames(rt reflect.Type) []string {
	base := rt
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	facade := base.PkgPath() == reflect.TypeOf(RValue{}).PkgPath()
	var names []string
	for i := 0; i < rt.NumMethod(); i++ {
		n := rt.Method(i).Name
		if facade && facadeInternalMethods[n] {
			continue
		}
		names = append(names, n)
	}
	return names
}

func (t *RType) NumMethod() int {
	if rt := t.methodRT(); rt != nil {
		return len(hostMethodNames(rt))
	}
	return len(exportedMethodNames(t.e.methodSet(t.td)))
}

func (t *RType) Method(i int) *Method {
	if rt := t.methodRT(); rt != nil {
		names := hostMethodNames(rt)
		if i < 0 || i >= len(names) {
			// An interface type's requirements are a plain list in
			// Go: out-of-range returns the zero Method instead of
			// panicking (matching the script-type path below).
			if rt.Kind() == reflect.Interface {
				return &Method{}
			}
			panic(&runtime.Panic{Value: "reflect: Method index out of range"})
		}
		m, _ := rt.MethodByName(names[i])
		if rt.Kind() == reflect.Interface {
			// Interface requirements carry no receiver in Type and
			// gc reports a zero Func — no implementation to call.
			return &Method{Name: m.Name, PkgPath: m.PkgPath,
				Type: t.e.hostTypeOf(m.Type), Index: i,
				Func: &RValue{e: t.e}}
		}
		f := t.e.wrapHost(nil, m.Func)
		f.mexpr = m.Name
		// gc's Method.Func.Type agrees with Method.Type — the
		// receiver keeps the enumerated type's pointer shape.
		f.rtSig = t.e.methodSigType(rt, m.Type)
		return &Method{Name: m.Name, PkgPath: m.PkgPath,
			Type: t.e.hostTypeOf(t.e.methodSigType(rt, m.Type)), Index: i,
			Func: f}
	}
	set := t.e.methodSet(t.td)
	names := exportedMethodNames(set)
	if i < 0 || i >= len(names) {
		// An interface type's requirements are a plain list in Go:
		// out-of-range returns the zero Method instead of panicking.
		if t.td.Kind == runtime.KindInterface {
			return &Method{}
		}
		panic(&runtime.Panic{Value: "reflect: Method index out of range"})
	}
	// Interface requirements carry no receiver in Method.Type —
	// func(int) string, not func(main.I, int) string.
	if t.td.Kind == runtime.KindInterface {
		// Go reports a zero Func for interface requirements — the
		// requirement has no implementation to call.
		return &Method{Name: names[i], Type: t.e.methodType(nil, set[names[i]]), Index: i,
			Func: &RValue{e: t.e}}
	}
	fn := set[names[i]]
	mt := t.e.methodType(t, fn)
	return &Method{Name: names[i], Type: mt, Index: i,
		Func: t.e.wrap(nil, t.methodFunc(fn, names[i]), nil, mt.td)}
}

func (t *RType) MethodByName(name string) (*Method, bool) {
	if rt := t.methodRT(); rt != nil {
		if facadeInternalMethods[name] {
			base := rt
			for base.Kind() == reflect.Pointer {
				base = base.Elem()
			}
			if base.PkgPath() == reflect.TypeOf(RValue{}).PkgPath() {
				return &Method{}, false
			}
		}
		m, ok := rt.MethodByName(name)
		if !ok {
			return &Method{}, false
		}
		// Index is the position in the filtered method list — the
		// same number Method(i) reports — not the host index.
		idx := -1
		for i, n := range hostMethodNames(rt) {
			if n == name {
				idx = i
				break
			}
		}
		if rt.Kind() == reflect.Interface {
			return &Method{Name: m.Name, PkgPath: m.PkgPath,
				Type: t.e.hostTypeOf(m.Type), Index: idx,
				Func: &RValue{e: t.e}}, true
		}
		f := t.e.wrapHost(nil, m.Func)
		f.mexpr = m.Name
		f.rtSig = t.e.methodSigType(rt, m.Type)
		return &Method{Name: m.Name, PkgPath: m.PkgPath,
			Type: t.e.hostTypeOf(t.e.methodSigType(rt, m.Type)), Index: idx,
			Func: f}, true
	}
	set := t.e.methodSet(t.td)
	for i, n := range exportedMethodNames(set) {
		if n == name {
			// interface requirements carry no receiver, like Method.
			if t.td.Kind == runtime.KindInterface {
				return &Method{Name: n, Type: t.e.methodType(nil, set[n]), Index: i,
					Func: &RValue{e: t.e}}, true
			}
			mt := t.e.methodType(t, set[n])
			return &Method{Name: n, Type: mt, Index: i,
				Func: t.e.wrap(nil, t.methodFunc(set[n], n), nil, mt.td)}, true
		}
	}
	// Go returns a zero Method value — m.Name reads "" where a nil
	// *Method would dereference nil.
	return &Method{}, false
}

// methodFunc returns the callable behind a script method's Method.Func:
// the method expression the compiler would emit for `t.td.name`, so
// pointer receiver types deref value receivers and promoted members
// re-select on the concrete receiver. Without a recorded caller the
// raw declared function still spells the signature, but Call on it
// reports the missing caller context.
func (t *RType) methodFunc(fn *runtime.Function, name string) runtime.Value {
	if vc := t.e.caller(); vc != nil {
		if m, ok := vc.Member(t.td, name); ok && m != nil {
			return m
		}
	}
	return fn
}

func (t *RType) Implements(u *RType) bool {
	// The check compares names and signatures: F(int) does not satisfy
	// a required F(string).
	if u != nil && u.Kind() != reflect.Interface {
		panic(&runtime.Panic{Value: "reflect: non-interface type passed to Type.Implements"})
	}
	if t.rt != nil {
		if u.rt != nil {
			return t.rt.Implements(u.rt)
		}
		return u.scriptImplements(t)
	}
	if u.rt != nil {
		return t.e.hostIfaceImplemented(u.rt, t.e.methodSet(t.td))
	}
	if u.td == nil {
		return false
	}
	if rt := t.e.hostRTOf(t.td); rt != nil {
		// a typedef-backed host type's methods live on the host
		// type, not in the script method set — check its real
		// method set directly (no intern: the td and its host
		// spelling share a key, so hostTypeOf could hand back
		// this same td-based RType and loop).
		return u.scriptImplements(&RType{e: t.e, rt: rt})
	}
	if len(u.td.MReqs) == 0 && len(u.td.IEmbeds) == 0 {
		return true
	}
	reqs := t.e.methodSet(u.td)
	have := t.e.methodSet(t.td)
	for name, req := range reqs {
		hm := have[name]
		if hm == nil || !runtime.SigIdentical(req, hm, reflectResolver{t.e}) {
			return false
		}
	}
	return true
}

// scriptImplements checks a host type's method set against this script
// interface type, name and signature alike.
func (t *RType) scriptImplements(h *RType) bool {
	if t.td == nil {
		return false
	}
	if len(t.td.MReqs) == 0 && len(t.td.IEmbeds) == 0 {
		return true
	}
	reqs := t.e.methodSet(t.td)
	have := map[string]reflect.Type{}
	for i := 0; i < h.rt.NumMethod(); i++ {
		m := h.rt.Method(i)
		// reflect reports a method-set Type with the receiver as first
		// parameter; interface requirements carry no receiver — strip it.
		mt := m.Type
		if mt.NumIn() > 0 {
			ins := make([]reflect.Type, 0, mt.NumIn()-1)
			for j := 1; j < mt.NumIn(); j++ {
				ins = append(ins, mt.In(j))
			}
			outs := make([]reflect.Type, 0, mt.NumOut())
			for j := 0; j < mt.NumOut(); j++ {
				outs = append(outs, mt.Out(j))
			}
			mt = reflect.FuncOf(ins, outs, mt.IsVariadic())
		}
		have[m.Name] = mt
	}
	for name, req := range reqs {
		ht, ok := have[name]
		if !ok || !t.e.sameFuncSig(req, ht) {
			return false
		}
	}
	return true
}

// hostIfaceImplemented reports whether the script method set satisfies
// a host interface type, name and signature alike.
func (e *Env) hostIfaceImplemented(u reflect.Type, have map[string]*runtime.Function) bool {
	for i := 0; i < u.NumMethod(); i++ {
		m := u.Method(i)
		hm := have[m.Name]
		if hm == nil || !e.sameFuncSig(hm, m.Type) {
			return false
		}
	}
	return true
}

// sameFuncSig reports whether a script member's declared signature
// matches a host method's reflect func type — in/out arity, each
// element type's canonical spelling, and variadicness. String
// comparison cannot work: reflect.Type.String writes `func() int`
// where the script spelling writes `func()(int)` — every cross-domain
// interface check whose method had results failed to match.
func (e *Env) sameFuncSig(fn *runtime.Function, ht reflect.Type) (same bool) {
	defer func() {
		// an unresolvable signature element is a mismatch, not a
		// panic — Implements never traps on either side
		if recover() != nil {
			same = false
		}
	}()
	st := e.methodType(nil, fn) // the declared signature, no receiver
	if st == nil || ht.Kind() != reflect.Func {
		return false
	}
	if st.NumIn() != ht.NumIn() || st.NumOut() != ht.NumOut() ||
		st.IsVariadic() != ht.IsVariadic() {
		return false
	}
	for i := 0; i < ht.NumIn(); i++ {
		if e.canonType(st.In(i)) != e.canonType(e.hostTypeOf(ht.In(i))) {
			return false
		}
	}
	for i := 0; i < ht.NumOut(); i++ {
		if e.canonType(st.Out(i)) != e.canonType(e.hostTypeOf(ht.Out(i))) {
			return false
		}
	}
	return true
}

// canonType renders a type canonically for cross-domain comparison:
// named types intern under "pkgpath.Name" on both sides already, while
// anonymous shapes must be spelled structurally — reflect.Type.String
// and the script spelling disagree on surface details (result parens,
// interface and struct braces, spacing).
func (e *Env) canonType(t *RType) string {
	if t == nil {
		return "<nil>"
	}
	if t.rt != nil && t.rt.Name() != "" {
		return t.key
	}
	// `any` is the empty interface on both sides — the script side
	// spells it like a name where the host side is anonymous.
	if t.td != nil && t.td.Name != "" && t.td.Name != "any" {
		return t.key
	}
	var sb strings.Builder
	switch t.Kind() {
	case reflect.Func:
		sb.WriteString("func(")
		for i := 0; i < t.NumIn(); i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(e.canonType(t.In(i)))
		}
		if t.IsVariadic() {
			sb.WriteByte('.')
		}
		sb.WriteString(")(")
		for i := 0; i < t.NumOut(); i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(e.canonType(t.Out(i)))
		}
		sb.WriteByte(')')
	case reflect.Interface:
		sb.WriteString("interface{")
		if t.rt != nil {
			for i := 0; i < t.rt.NumMethod(); i++ {
				m := t.rt.Method(i)
				if !ast.IsExported(m.Name) {
					continue // symmetric with the script side's exported-only set
				}
				sb.WriteString(m.Name)
				sb.WriteString(e.canonType(e.hostTypeOf(m.Type)))
				sb.WriteByte(';')
			}
		} else {
			set := t.e.methodSet(t.td)
			for _, name := range exportedMethodNames(set) {
				sb.WriteString(name)
				// interface methods carry no receiver — build the
				// bare signature, not methodType's receiver form
				sb.WriteString(e.canonType(e.methodType(nil, set[name])))
				sb.WriteByte(';')
			}
		}
		sb.WriteString("}")
	case reflect.Struct:
		sb.WriteString("struct{")
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				sb.WriteByte('&')
			}
			if f.PkgPath != "" {
				sb.WriteString(f.PkgPath)
				sb.WriteByte('.')
			}
			sb.WriteString(f.Name)
			sb.WriteByte(' ')
			sb.WriteString(e.canonType(f.Type))
			if f.Tag != "" {
				sb.WriteByte(' ')
				sb.WriteString(string(f.Tag))
			}
			sb.WriteByte(';')
		}
		sb.WriteString("}")
	default:
		// pointer/slice/map/chan/array/basic spellings agree across
		// domains — the interning key is already canonical.
		sb.WriteString(t.key)
	}
	return sb.String()
}

func (t *RType) AssignableTo(u *RType) bool {
	// Go's assignment rule: identical types, an interface the type
	// implements, or identical underlying types where at least one side
	// is unnamed (a named S -> []int, never S -> T).
	if u == nil {
		return false
	}
	if t == u || t.key == u.key {
		return true
	}
	if t.rt != nil && u.rt != nil {
		return t.rt.AssignableTo(u.rt)
	}
	if u.Kind() == reflect.Interface {
		return t.Implements(u)
	}
	if t.td != nil && u.td != nil &&
		runtime.TypUnderlyingSpelling(t.td) == runtime.TypUnderlyingSpelling(u.td) {
		return t.td.Name == "" || u.td.Name == ""
	}
	return false
}

func (t *RType) ConvertibleTo(u *RType) bool {
	// Go's conversion rules: identical underlying types (struct tags
	// ignored, unlike assignment), non-complex numerics between
	// themselves, complex only to complex, integer<->string (rune),
	// []byte/[]rune<->string, and a slice to an array or array pointer
	// of the same element. Anything else is false (string -> []int,
	// []int -> string, int -> complex).
	if u == nil {
		return false
	}
	if t == u || t.key == u.key {
		return true
	}
	if t.rt != nil && u.rt != nil {
		return t.rt.ConvertibleTo(u.rt)
	}
	if t.td != nil && u.td != nil && runtime.ConvertibleTo(t.td, u.td, reflectResolver{t.e}) {
		return true
	}
	// the td-level judgment covers script typedefs; for host/rt hybrids
	// (one side rt-only) fall back to kind-level rules.
	tk, uk := t.Kind(), u.Kind()
	complexKind := func(k reflect.Kind) bool {
		return k == reflect.Complex64 || k == reflect.Complex128
	}
	if complexKind(tk) || complexKind(uk) {
		return complexKind(tk) && complexKind(uk)
	}
	intKind := func(k reflect.Kind) bool {
		return k >= reflect.Int && k <= reflect.Uintptr
	}
	floatKind := func(k reflect.Kind) bool {
		return k == reflect.Float32 || k == reflect.Float64
	}
	if (intKind(tk) || floatKind(tk)) && (intKind(uk) || floatKind(uk)) {
		return true
	}
	runeSlice := func(rt *RType) bool {
		if rt.Kind() != reflect.Slice {
			return false
		}
		et := rt.Elem()
		if et == nil {
			return false
		}
		switch et.Kind() {
		case reflect.Uint8, reflect.Int32:
			return true
		}
		return false
	}
	if intKind(tk) && uk == reflect.String {
		return true // integer converts to a one-rune string
	}
	if tk == reflect.String && runeSlice(u) {
		return true // string -> []byte / []rune
	}
	if runeSlice(t) && uk == reflect.String {
		return true // []byte / []rune -> string
	}
	if tk == reflect.Slice && uk == reflect.Array {
		// slice -> array needs identical element types
		return t.Elem() != nil && u.Elem() != nil && t.Elem().key == u.Elem().key
	}
	if tk == reflect.Slice && uk == reflect.Ptr {
		// slice -> *array needs identical element types
		if ue := u.Elem(); ue != nil && ue.Kind() == reflect.Array {
			return t.Elem() != nil && ue.Elem() != nil && t.Elem().key == ue.Elem().key
		}
		return false
	}
	return false
}

func (t *RType) Comparable() bool {
	if t.rt != nil {
		return t.rt.Comparable()
	}
	return t.e.comparableTd(t.td, map[*runtime.TypeDef]bool{})
}

func (t *RType) CanSeq() bool {
	if t.rt != nil {
		return t.rt.CanSeq()
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Array, reflect.Slice, reflect.Chan, reflect.String, reflect.Map:
		return true
	case reflect.Func:
		return t.canRange(1)
	case reflect.Ptr:
		return t.Elem().Kind() == reflect.Array
	}
	return false
}

func (t *RType) CanSeq2() bool {
	if t.rt != nil {
		return t.rt.CanSeq2()
	}
	switch t.Kind() {
	case reflect.Array, reflect.Slice, reflect.String, reflect.Map:
		return true
	case reflect.Func:
		return t.canRange(2)
	case reflect.Ptr:
		return t.Elem().Kind() == reflect.Array
	}
	return false
}

// canRange mirrors reflect's canRangeFunc: a func type is a Seq
// producer when it takes exactly one func parameter — the yield —
// which itself takes `seq` inputs and returns one unnamed bool.
func (t *RType) canRange(seq int) bool {
	if t.NumIn() != 1 || t.NumOut() != 0 {
		return false
	}
	y := t.In(0)
	if y.Kind() != reflect.Func || y.NumIn() != seq || y.NumOut() != 1 {
		return false
	}
	o := y.Out(0)
	return o.Kind() == reflect.Bool && o.PkgPath() == ""
}

// Fields returns reflect.Type.Fields' iterator as a script function
// value: a script can range over it or call it with a yield function.
func (t *RType) Fields() runtime.Value {
	return t.iterAccessors("Fields", "struct", reflect.Struct, t.NumField,
		func(rt reflect.Type, i int) any { return rt.Field(i) },
		func(i int) any { return t.Field(i) })
}

// Methods returns reflect.Type.Methods' iterator as a script function
// value, like [RType.Fields].
func (t *RType) Methods() runtime.Value {
	return t.iterValues("reflect.Type.Methods", t.NumMethod(), func(i int) any {
		if t.rt != nil {
			return t.rt.Method(i)
		}
		return t.Method(i)
	})
}

// Ins returns reflect.Type.Ins' iterator as a script function value,
// like [RType.Fields].
func (t *RType) Ins() runtime.Value {
	return t.iterAccessors("Ins", "func", reflect.Func, t.NumIn,
		func(rt reflect.Type, i int) any { return rt.In(i) },
		func(i int) any { return t.In(i) })
}

// Outs returns reflect.Type.Outs' iterator as a script function value,
// like [RType.Fields].
func (t *RType) Outs() runtime.Value {
	return t.iterAccessors("Outs", "func", reflect.Func, t.NumOut,
		func(rt reflect.Type, i int) any { return rt.Out(i) },
		func(i int) any { return t.Out(i) })
}

// iterAccessors shares Fields/Ins/Outs: the kind guard (want kind or
// panic "reflect: <name> of non-<kind> type") then iterValues over
// the host or script element accessor.
func (t *RType) iterAccessors(name, noun string, want reflect.Kind, num func() int,
	hostAt func(reflect.Type, int) any, scriptAt func(int) any) runtime.Value {
	if t.Kind() != want {
		plain("reflect: %s of non-%s type %s", name, noun, t)
	}
	return t.iterValues("reflect.Type."+name, num(), func(i int) any {
		if t.rt != nil {
			return hostAt(t.rt, i)
		}
		return scriptAt(i)
	})
}

// iterValues is the shared yield-driver for the type iterator
// accessors: `at` produces element i, boxed as a GoValue so untyped
// range variables resolve members through host dispatch (the same
// trick Value.Seq's yield uses — StructField/Method/RType all answer
// their fields and methods via hostMember).
func (t *RType) iterValues(name string, n int, at func(i int) any) runtime.Value {
	return &runtime.BuiltinFunc{Name: name, Fn: func(vc runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		if len(args) != 1 {
			return nil, fmt.Errorf("%s expects a yield function", name)
		}
		for i := 0; i < n; i++ {
			r, err := vc.Call(args[0], []runtime.Value{&runtime.GoValue{V: at(i)}})
			if err != nil {
				return nil, err
			}
			if b, ok := r.(bool); !ok || !b {
				return nil, nil
			}
		}
		return nil, nil
	}}
}

// comparableTd recurses the comparable rule through struct fields and
// composite elements.
func (e *Env) comparableTd(td *runtime.TypeDef, seen map[*runtime.TypeDef]bool) bool {
	if td == nil || seen[td] {
		return true
	}
	seen[td] = true
	switch e.kindOfTd(td) {
	case reflect.Slice, reflect.Map, reflect.Func:
		return false
	case reflect.Struct:
		for _, ft := range e.fieldTypes(td) {
			if ft != nil && !e.comparableTd(ft, seen) {
				return false
			}
		}
		return true
	case reflect.Array:
		return e.comparableTd(e.elemOf(td), seen)
	case reflect.Pointer:
		// pointers are always comparable — only the pointer identity
		// is compared, never the pointee (an array IS different: its
		// elements are compared one by one).
		return true
	}
	return true
}

func (t *RType) Bits() int {
	if t.rt != nil {
		return t.rt.Bits()
	}
	switch t.Kind() {
	case reflect.Int8, reflect.Uint8:
		return 8
	case reflect.Int16, reflect.Uint16:
		return 16
	case reflect.Int32, reflect.Uint32, reflect.Float32:
		return 32
	case reflect.Int64, reflect.Uint64, reflect.Float64, reflect.Complex64:
		return 64
	case reflect.Complex128:
		return 128
	case reflect.Int, reflect.Uint:
		return 64 // minigo's int is int64
	}
	trap("Bits of non-arithmetic Type %s", t.String())
	return 0
}

// Align reports a script type's alignment as laid out on amd64; a host
// type reports the host's.
func (t *RType) Align() int {
	// Scalars align by size, aggregates by their widest member
	// (slice/map/chan/func/ptr/iface are all word-sized).
	if t.rt != nil {
		return t.rt.Align()
	}
	return t.alignOf()
}

// FieldAlign is [RType.Align] for a script type; the two differ only on
// 32-bit targets.
func (t *RType) FieldAlign() int {
	if t.rt != nil {
		return t.rt.FieldAlign()
	}
	return t.alignOf()
}

// OffsetOf reports the byte offset of field name in the struct type td,
// laid out on amd64 by declared field types, as StructField.Offset
// reports it. It reports an error if td is not a struct type or has no
// such field.
func OffsetOf(h Hooks, td *runtime.TypeDef, name string) (uintptr, error) {
	// Field positions come from the typedefs' declared size and
	// alignment, never the runtime values a field happens to hold.
	e := &Env{h: h}
	st := e.structTd(td)
	if st == nil || len(st.Fields) == 0 {
		return 0, fmt.Errorf("unsafe.Offsetof: %s is not a struct type", td.Name)
	}
	fts := e.fieldTypes(st)
	for i, f := range st.Fields {
		if f == name {
			return fieldOffset(e.rtypeOf(st), fts, i), nil
		}
	}
	return 0, fmt.Errorf("unsafe.Offsetof: %s has no field %s", st.Name, name)
}

// SizeOf reports the size of td as laid out on amd64, the layout
// [OffsetOf] uses.
func SizeOf(h Hooks, td *runtime.TypeDef) uintptr {
	// Scalars by width, 16-byte string/interface headers, 24-byte
	// slices, padded structs.
	e := &Env{h: h}
	return e.rtypeOf(td).sizeOf()
}

// HasPointers reports whether a value of td contains pointers — the
// scan/noscan split gc's growslice applies for the malloc header.
func HasPointers(h Hooks, td *runtime.TypeDef) bool {
	e := &Env{h: h}
	return e.rtypeOf(td).hasPointers()
}

// CallerHooks builds the hooks a VMCaller already answers — ElemOf,
// FieldTypes and ResolveType — so intrinsics in packages that cannot
// reach the engine (builtin, internal/stdlib) can still size and scan
// declared types.
func CallerHooks(v runtime.VMCaller) Hooks {
	return Hooks{
		ElemOf:      v.ElemOf,
		FieldTypes:  v.FieldTypes,
		ResolveType: v.ResolveType,
		Underlying:  v.Underlying,
	}
}

// AlignOf reports the alignment of td as laid out on amd64, the layout
// [OffsetOf] uses.
func AlignOf(h Hooks, td *runtime.TypeDef) uintptr {
	// The widest alignment among a struct's fields (empty struct → 1),
	// an array's element, 8 for word-sized values and pointers.
	e := &Env{h: h}
	return uintptr(e.rtypeOf(td).alignOf())
}

// fieldOffset lays out the struct's fields on amd64 up to field i:
// each field sits at the next offset aligned to its own alignment. fts
// is the field-type list of the typedef carrying the field list — a
// type defined over another struct resolves it through the base struct.
func fieldOffset(t *RType, fts []*runtime.TypeDef, i int) uintptr {
	var off uintptr
	for j := 0; j < i && j < len(fts); j++ {
		if fts[j] == nil {
			continue
		}
		fj := t.e.rtypeOf(fts[j])
		off = roundUp(off, uintptr(fj.alignOf())) + fj.sizeOf()
	}
	if i < len(fts) && fts[i] != nil {
		off = roundUp(off, uintptr(t.e.rtypeOf(fts[i]).alignOf()))
	}
	return off
}

func roundUp(off, a uintptr) uintptr {
	if a == 0 {
		return off
	}
	return (off + a - 1) / a * a
}

// sizeOf computes the amd64 size of a script type in bytes: scalars
// by width, string/interface headers 16, slices 24, arrays elem*N,
// structs padded to their own alignment; word-sized containers and
// pointers are 8.
// Size reports the type's storage size — the host layout when backed by
// a real reflect.Type, the amd64 estimate otherwise (reflect.Type.Size).
func (t *RType) Size() uintptr {
	if t.rt != nil {
		return t.rt.Size()
	}
	return t.sizeOf()
}

func (t *RType) sizeOf() uintptr {
	switch t.Kind() {
	case reflect.Bool, reflect.Int8, reflect.Uint8:
		return 1
	case reflect.Int16, reflect.Uint16:
		return 2
	case reflect.Int32, reflect.Uint32, reflect.Float32:
		return 4
	case reflect.Int, reflect.Uint, reflect.Int64, reflect.Uint64,
		reflect.Uintptr, reflect.Float64, reflect.Complex64:
		return 8
	case reflect.Complex128:
		return 16
	case reflect.String, reflect.Interface:
		return 16
	case reflect.Slice:
		return 24
	case reflect.Array:
		return t.Elem().sizeOf() * uintptr(t.Len())
	case reflect.Struct:
		var off uintptr
		for i := 0; i < t.NumField(); i++ {
			ft := t.Field(i).Type
			off = roundUp(off, uintptr(ft.alignOf())) + ft.sizeOf()
		}
		// gc pads a struct whose LAST field is zero-size so the
		// field's address stays in bounds — only when a preceding field
		// occupied space: struct{i int64; s struct{}} is 16, while
		// struct{s struct{}} stays 0.
		if n := t.NumField(); off > 0 && n > 0 && t.Field(n-1).Type.sizeOf() == 0 {
			off++
		}
		return roundUp(off, uintptr(t.alignOf()))
	}
	return 8
}

// hasPointers reports whether a value of the type contains pointers —
// the scan/noscan split the runtime's growslice uses to reserve an
// 8-byte malloc header (go1.26+). Containers and strings hold data
// pointers; numbers, bool and uintptr do not.
func (t *RType) hasPointers() bool {
	switch t.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Chan, reflect.Func,
		reflect.Slice, reflect.String, reflect.Interface,
		reflect.UnsafePointer:
		return true
	case reflect.Array:
		return t.Elem().hasPointers()
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).Type.hasPointers() {
				return true
			}
		}
	}
	return false
}

// alignOf computes the amd64 alignment of a script type. A struct
// aligns to its widest field (empty struct → 1); an array to its
// element; word-sized containers and pointers to 8.
func (t *RType) alignOf() int {
	switch t.Kind() {
	case reflect.Int8, reflect.Uint8, reflect.Bool:
		return 1
	case reflect.Int16, reflect.Uint16:
		return 2
	case reflect.Int32, reflect.Uint32, reflect.Float32, reflect.Complex64:
		return 4
	case reflect.Struct:
		n := 1
		for i := 0; i < t.NumField(); i++ {
			if a := t.Field(i).Type.alignOf(); a > n {
				n = a
			}
		}
		return n
	case reflect.Array:
		return t.Elem().alignOf()
	}
	return 8
}

func (t *RType) ChanDir() reflect.ChanDir {
	// An Anon ChanType carries the declared direction; a bare chan
	// defaults to BothDir. Go panics on non-chan types.
	if t.rt != nil {
		return t.rt.ChanDir()
	}
	if t.Kind() != reflect.Chan {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect: ChanDir of non-chan type %s", t.String())})
	}
	if ct, ok := t.e.exprOf(t.td).(*ast.ChanType); ok {
		switch ct.Dir {
		case ast.RECV:
			return reflect.RecvDir
		case ast.SEND:
			return reflect.SendDir
		}
	}
	return reflect.BothDir
}

func (t *RType) NumIn() int {
	if t.rt != nil {
		return t.rt.NumIn()
	}
	ft := funcSig(t)
	if ft == nil {
		trap("NumIn of non-func type %s", t.String())
	}
	return syntax.FieldCount(ft.Params)
}

func (t *RType) NumOut() int {
	if t.rt != nil {
		return t.rt.NumOut()
	}
	ft := funcSig(t)
	if ft == nil {
		trap("NumOut of non-func type %s", t.String())
	}
	return syntax.FieldCount(ft.Results)
}

func (t *RType) In(i int) *RType {
	if t.rt != nil {
		return t.e.hostTypeOf(t.rt.In(i))
	}
	if funcSig(t) == nil {
		trap("In of non-func type %s", t.String())
	}
	return t.resolveIn(funcParam(t, i, false))
}

func (t *RType) Out(i int) *RType {
	if t.rt != nil {
		return t.e.hostTypeOf(t.rt.Out(i))
	}
	if funcSig(t) == nil {
		trap("Out of non-func type %s", t.String())
	}
	return t.resolveIn(funcParam(t, i, true))
}

func (t *RType) IsVariadic() bool {
	if t.rt != nil {
		return t.rt.IsVariadic()
	}
	ft := funcSig(t)
	if ft == nil {
		trap("IsVariadic of non-func type %s", t.String())
	}
	if ft.Params == nil || len(ft.Params.List) == 0 {
		return false
	}
	last := ft.Params.List[len(ft.Params.List)-1]
	_, is := last.Type.(*ast.Ellipsis)
	return is
}

func (t *RType) resolveIn(x ast.Expr) *RType {
	if t.e.h.ResolveType == nil {
		trap("minireflect: func signature resolution needs ResolveType hook")
	}
	if ell, ok := x.(*ast.Ellipsis); ok {
		// a variadic param's In type is the []T slice, like Go.
		td, err := t.e.resolveExpr(t.td, ell.Elt)
		if err != nil {
			trap("minireflect: %s", err)
		}
		return t.e.rtypeOf(&runtime.TypeDef{Kind: runtime.KindSlice, Elem: td,
			Anon: &ast.ArrayType{Elt: ell.Elt}})
	}
	td, err := t.e.resolveExpr(t.td, x)
	if err != nil {
		trap("minireflect: %s", err)
	}
	return t.e.rtypeOf(td)
}

// funcSig unwraps a func typedef to its FuncType AST.
func funcSig(t *RType) *ast.FuncType {
	if t.td == nil {
		return nil
	}
	if ft, ok := t.td.Anon.(*ast.FuncType); ok {
		return ft
	}
	return nil
}

// funcParam resolves the i'th param/result expr of a FuncType,
// counting unnamed entries singly. The caller must have verified
// funcSig is non-nil; an out-of-range index panics like Go — a bare
// 'index out of range' runtime error, not a reflect-worded one.
func funcParam(t *RType, i int, results bool) ast.Expr {
	ft := funcSig(t)
	list := ft.Params
	if results {
		list = ft.Results
	}
	n := 0
	if list != nil {
		for _, f := range list.List {
			cnt := len(f.Names)
			if cnt == 0 {
				cnt = 1
			}
			if i >= n && i < n+cnt {
				return f.Type
			}
			n += cnt
		}
	}
	panic(runtime.BoundsPanic(i, n))
}

// reflectResolver adapts the facade's Hooks to runtime.TypeResolver so
// Implements compares signatures with the same semantic normalization
// (alias peel, interface method-set order) the VM applies. The facade
// has no IfaceSigs hook, but its MethodSet hook returns an interface
// typedef's requirement funcs — the same signature map.
type reflectResolver struct{ e *Env }

func (r reflectResolver) PeelAlias(td *runtime.TypeDef) *runtime.TypeDef {
	for i := 0; td != nil && td.Kind == runtime.KindAlias && i < 32; i++ {
		if r.e.h.AliasOf == nil {
			return td
		}
		nt, err := r.e.h.AliasOf(td)
		if err != nil || nt == nil || nt == td {
			return td
		}
		td = nt
	}
	return td
}

func (r reflectResolver) IfaceReqs(td *runtime.TypeDef) (map[string]bool, error) {
	if r.e.h.IfaceReqs == nil {
		return nil, fmt.Errorf("no IfaceReqs hook")
	}
	return r.e.h.IfaceReqs(td)
}

func (r reflectResolver) IfaceSigs(td *runtime.TypeDef) (map[string]*runtime.Function, error) {
	if r.e.h.MethodSet == nil {
		return nil, fmt.Errorf("no MethodSet hook")
	}
	return r.e.h.MethodSet(td)
}

func (r reflectResolver) ResolveType(from *runtime.TypeDef, x ast.Expr) (*runtime.TypeDef, error) {
	if r.e.h.ResolveType == nil {
		return nil, nil
	}
	return r.e.h.ResolveType(from, x)
}

func (r reflectResolver) ElemOf(td *runtime.TypeDef) (*runtime.TypeDef, error) {
	if r.e.h.ElemOf == nil {
		return nil, nil
	}
	return r.e.h.ElemOf(td)
}

func (r reflectResolver) Underlying(td *runtime.TypeDef) *runtime.TypeDef {
	for i := 0; td != nil && (td.Kind == runtime.KindAlias || td.Kind == runtime.KindNamedBasic) && r.e.h.Underlying != nil && i < 32; i++ {
		u, err := r.e.h.Underlying(td)
		if err != nil || u == nil || u == td {
			break
		}
		td = u
	}
	return td
}
