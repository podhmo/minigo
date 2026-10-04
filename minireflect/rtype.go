package minireflect

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/podhmo/minigo/runtime"
)

// RType is the facade's reflect.Type: an interned view over either a
// script *runtime.TypeDef or a host reflect.Type. Two RTypes covering
// the same canonical type are pointer-equal so `a == b` works through
// the GoValue identity the VM compares.
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

// StructField mirrors reflect.StructField for script-facing field
// metadata. Tag is the real reflect.StructTag so Tag.Get works.
type StructField struct {
	Name      string
	PkgPath   string
	Type      *RType
	Tag       reflect.StructTag
	Offset    uintptr
	Index     []int
	Anonymous bool
}

// Method mirrors reflect.Method for the subset the facade reports.
// Func is the method-expression value: on a pointer receiver type a
// value-receiver member derefs its *T argument, and a promoted or
// interface member re-selects on the receiver argument — the same
// values `(*T).M`/`T.M` compile to.
type Method struct {
	Name    string
	PkgPath string
	Type    *RType
	Index   int
	Func    *RValue
}

// rtypeOf interns a script typedef.
func (e *Env) rtypeOf(td *runtime.TypeDef) *RType {
	return e.intern(e.keyOf(td), td, nil)
}

// hostTypeOf interns a host reflect.Type.
func (e *Env) hostTypeOf(rt reflect.Type) *RType {
	return e.intern(hostTypeKey(rt), nil, rt)
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
// share a declared name but are different types.
func (e *Env) bindsKeyOf(td *runtime.TypeDef) string {
	if len(td.TParams) == 0 || len(td.Binds) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("[")
	wrote := false
	for _, tp := range td.TParams {
		bv, ok := td.Binds[tp]
		if !ok {
			continue
		}
		atd, _ := bv.(*runtime.TypeDef)
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
			Anon: st, Pkg: from.Pkg, File: from.File, Binds: from.Binds}, nil
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

// methodSig spells a member's signature for Implements comparison — the
// declared FuncType under the member's own package/binds context.
func methodSig(m *runtime.Function) string {
	if m == nil || m.Decl == nil {
		return ""
	}
	ctx := &runtime.TypeDef{Pkg: m.Pkg, File: m.File, Binds: m.Binds}
	return runtime.TypSpelling(m.Decl.Type, ctx)
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

// ---- reflect.Type methods ----

// Kind reports the type's kind.
func (t *RType) Kind() reflect.Kind {
	if t.rt != nil {
		return t.rt.Kind()
	}
	if t.isArray {
		return reflect.Array
	}
	return t.e.kindOfTd(t.td)
}

// Name reports the type's unqualified name.
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
		}
	}
	return name
}

// PkgPath reports the import path of the type's package.
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

// String renders the type the way reflect.Type.String does.
func (t *RType) String() string {
	if t.rt != nil {
		return t.rt.String()
	}
	return runtime.DisplayName(t.td)
}

// Elem resolves the element type.
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

// Key resolves a map type's key type.
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

// Len reports an array's length.
func (t *RType) Len() int {
	if t.rt != nil {
		return t.rt.Len()
	}
	if t.isArray {
		return t.alen
	}
	if at, ok := t.td.Anon.(*ast.ArrayType); ok && at.Len != nil {
		if bl, ok := at.Len.(*ast.BasicLit); ok && bl.Kind == token.INT {
			if n, err := strconv.Atoi(bl.Value); err == nil {
				return n
			}
		}
	}
	trap("Len of non-array type %s", t.String())
	return 0
}

// NumField reports a struct's field count.
func (t *RType) NumField() int {
	if t.rt != nil {
		return t.rt.NumField()
	}
	if t.Kind() != reflect.Struct {
		trap("NumField of non-struct type %s", t.String())
	}
	return len(t.td.Fields)
}

// Field reports a struct's i'th field.
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
	if i < 0 || i >= len(t.td.Fields) {
		panic(&runtime.Panic{Value: "reflect: Field index out of bounds"})
	}
	fts := t.e.fieldTypes(t.td)
	var ft *RType
	if i < len(fts) {
		ft = t.e.rtypeOf(fts[i])
	}
	embedded := false
	for _, ei := range t.td.EmbedIdx {
		if ei == i {
			embedded = true
			break
		}
	}
	name := t.td.Fields[i]
	sf := &StructField{
		Name:      name,
		Type:      ft,
		Offset:    fieldOffset(t, i),
		Index:     []int{i},
		Anonymous: embedded,
	}
	if name != "" && !unicode.IsUpper(rune(name[0])) {
		// unexported field — embedded or not — carries the declaring
		// package path, like Go's reflect.
		sf.PkgPath = t.PkgPath()
	}
	if t.td.FTags != nil {
		sf.Tag = reflect.StructTag(t.td.FTags[name])
	}
	return sf
}

// FieldByIndex resolves a nested field path.
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

// FieldByName looks up a field by name, including promotion through
// embedded fields.
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
	for i, fn := range t.td.Fields {
		if fn == name {
			return t.Field(i), true
		}
	}
	fts := t.e.fieldTypes(t.td)
	for _, ei := range t.td.EmbedIdx {
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

// FieldByNameFunc finds the field whose name satisfies match, including
// promotion through embedded fields. The traversal is Go's own: breadth
// first search, one depth level at a time, where two matches at the same
// depth annihilate each other and a struct type reachable through
// multiple embedded paths annihilates its own matches.
func (t *RType) FieldByNameFunc(match func(string) bool) (*StructField, bool) {
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
	next := []scan{{td: t.td}}
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
			st := sc.td
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

// NumMethod reports the exported method count — pointer-receiver
// members count only under a pointer type, like Go's method sets.
func (t *RType) NumMethod() int {
	if t.rt != nil {
		return t.rt.NumMethod()
	}
	return len(exportedMethodNames(t.e.methodSet(t.td)))
}

// Method reports the i'th exported method in sorted order, carrying its
// signature (the receiver is the first parameter, as Go reports it).
func (t *RType) Method(i int) *Method {
	if t.rt != nil {
		m := t.rt.Method(i)
		return &Method{Name: m.Name, PkgPath: m.PkgPath,
			Type: t.e.hostTypeOf(m.Type), Index: m.Index,
			Func: t.e.wrapHost(nil, m.Func)}
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

// MethodByName looks up an exported method by name — like Go's reflect,
// unexported members are not reachable through the facade.
func (t *RType) MethodByName(name string) (*Method, bool) {
	if t.rt != nil {
		m, ok := t.rt.MethodByName(name)
		if !ok {
			return &Method{}, false
		}
		return &Method{Name: m.Name, PkgPath: m.PkgPath,
			Type: t.e.hostTypeOf(m.Type), Index: m.Index,
			Func: t.e.wrapHost(nil, m.Func)}, true
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

// Implements reports whether the type implements interface u. The check
// compares names AND signatures, so F(int) no longer satisfies a
// required F(string). A non-interface argument panics like Go.
func (t *RType) Implements(u *RType) bool {
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
	if len(u.td.MReqs) == 0 && len(u.td.IEmbeds) == 0 {
		return true
	}
	reqs := t.e.methodSet(u.td)
	have := t.e.methodSet(t.td)
	for name, req := range reqs {
		hm := have[name]
		if hm == nil || methodSig(hm) != methodSig(req) {
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

// AssignableTo reports Go's assignment rule: identical types, an
// interface the type implements, or identical underlying types where
// at least one side is unnamed (a named S -> []int, never S -> T).
func (t *RType) AssignableTo(u *RType) bool {
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

// ConvertibleTo reports reflect's conversion rules — narrower than the
// language's: identical underlying types, non-complex numerics between
// themselves, complex only to complex, integer<->string (rune), and
// []byte/[]rune<->string. A slice also converts to an array of the same
// element. Anything else is false (string -> []int, []int -> string,
// int -> complex all report false like Go).
func (t *RType) ConvertibleTo(u *RType) bool {
	if u == nil {
		return false
	}
	if t == u || t.key == u.key {
		return true
	}
	if t.rt != nil && u.rt != nil {
		return t.rt.ConvertibleTo(u.rt)
	}
	if t.td != nil && u.td != nil &&
		runtime.TypUnderlyingSpelling(t.td) == runtime.TypUnderlyingSpelling(u.td) {
		return true
	}
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

// Comparable reports whether values of the type can be compared — the
// check recurses: a struct is comparable only when every field is, an
// array only when its element is, like Go's own rule.
func (t *RType) Comparable() bool {
	if t.rt != nil {
		return t.rt.Comparable()
	}
	return t.e.comparableTd(t.td, map[*runtime.TypeDef]bool{})
}

// CanSeq reports whether a value of this type produces an
// iter.Seq[Value]: ints and uints, array, slice, chan, string, map,
// ptr-to-array, and func(yield-1) producers.
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

// CanSeq2 reports the same for iter.Seq2[Value, Value]: array, slice,
// string, map, ptr-to-array, and func(yield-2) producers.
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

// Fields is Go 1.26's iter.Seq[StructField] over a struct type's
// fields — a script-callable yield producer like Value.Seq, so both
// for-range and direct calls enumerate Field(i) in order.
func (t *RType) Fields() runtime.Value {
	if t.Kind() != reflect.Struct {
		plain("reflect: Fields of non-struct type %s", t)
	}
	return t.iterValues("reflect.Type.Fields", t.NumField(), func(i int) any {
		if t.rt != nil {
			return t.rt.Field(i)
		}
		return t.Field(i)
	})
}

// Methods enumerates the type's method set — no kind gate; a type
// with no methods simply yields nothing.
func (t *RType) Methods() runtime.Value {
	return t.iterValues("reflect.Type.Methods", t.NumMethod(), func(i int) any {
		if t.rt != nil {
			return t.rt.Method(i)
		}
		return t.Method(i)
	})
}

// Ins enumerates a func type's parameter types.
func (t *RType) Ins() runtime.Value {
	if t.Kind() != reflect.Func {
		plain("reflect: Ins of non-func type %s", t)
	}
	return t.iterValues("reflect.Type.Ins", t.NumIn(), func(i int) any {
		if t.rt != nil {
			return t.rt.In(i)
		}
		return t.In(i)
	})
}

// Outs enumerates a func type's result types.
func (t *RType) Outs() runtime.Value {
	if t.Kind() != reflect.Func {
		plain("reflect: Outs of non-func type %s", t)
	}
	return t.iterValues("reflect.Type.Outs", t.NumOut(), func(i int) any {
		if t.rt != nil {
			return t.rt.Out(i)
		}
		return t.Out(i)
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

// Bits reports the type's size in bits for sized numerics.
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

// Align reports the type's alignment for a 64-bit target: scalars by
// size, aggregates by their widest member (slice/map/chan/func/ptr/
// iface are all word-sized).
func (t *RType) Align() int {
	if t.rt != nil {
		return t.rt.Align()
	}
	return t.alignOf()
}

// FieldAlign reports the field alignment — identical to Align on
// amd64 (the platforms where they differ only affect 32-bit targets).
func (t *RType) FieldAlign() int {
	if t.rt != nil {
		return t.rt.FieldAlign()
	}
	return t.alignOf()
}

// fieldOffset lays out the struct's fields on amd64 up to field i:
// each field sits at the next offset aligned to its own alignment.
func fieldOffset(t *RType, i int) uintptr {
	fts := t.e.fieldTypes(t.td)
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
func (t *RType) sizeOf() uintptr {
	switch t.Kind() {
	case reflect.Bool, reflect.Int8, reflect.Uint8:
		return 1
	case reflect.Int16, reflect.Uint16:
		return 2
	case reflect.Int32, reflect.Uint32, reflect.Float32, reflect.Complex64:
		return 4
	case reflect.Int, reflect.Uint, reflect.Int64, reflect.Uint64,
		reflect.Uintptr, reflect.Float64, reflect.Complex128:
		return 8
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

// ChanDir reports a chan type's direction — an Anon ChanType carries
// the declared direction; a bare chan defaults to BothDir. Go panics
// on non-chan types.
func (t *RType) ChanDir() reflect.ChanDir {
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

// NumIn reports a func type's input count.
func (t *RType) NumIn() int {
	if t.rt != nil {
		return t.rt.NumIn()
	}
	ft := funcSig(t)
	if ft == nil {
		trap("NumIn of non-func type %s", t.String())
	}
	if ft.Params == nil {
		return 0
	}
	n := 0
	for _, f := range ft.Params.List {
		if len(f.Names) > 0 {
			n += len(f.Names)
		} else {
			n++
		}
	}
	return n
}

// NumOut reports a func type's result count.
func (t *RType) NumOut() int {
	if t.rt != nil {
		return t.rt.NumOut()
	}
	ft := funcSig(t)
	if ft == nil {
		trap("NumOut of non-func type %s", t.String())
	}
	if ft.Results == nil {
		return 0
	}
	n := 0
	for _, f := range ft.Results.List {
		if len(f.Names) > 0 {
			n += len(f.Names)
		} else {
			n++
		}
	}
	return n
}

// In resolves a func type's i'th input type.
func (t *RType) In(i int) *RType {
	if t.rt != nil {
		return t.e.hostTypeOf(t.rt.In(i))
	}
	if funcSig(t) == nil {
		trap("In of non-func type %s", t.String())
	}
	return t.resolveIn(funcParam(t, i, false))
}

// Out resolves a func type's i'th output type.
func (t *RType) Out(i int) *RType {
	if t.rt != nil {
		return t.e.hostTypeOf(t.rt.Out(i))
	}
	if funcSig(t) == nil {
		trap("Out of non-func type %s", t.String())
	}
	return t.resolveIn(funcParam(t, i, true))
}

// IsVariadic reports whether a func type is variadic.
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
	if i < 0 {
		panic(runtime.RuntimePanic(fmt.Sprintf("index out of range [%d]", i)))
	}
	panic(runtime.BoundsPanic(i, n))
	return nil
}
