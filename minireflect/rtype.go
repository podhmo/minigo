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
	Index     []int
	Anonymous bool
}

// Method mirrors reflect.Method for the subset the facade reports.
// Func stays nil for script methods — calling them goes through the
// owning value's MethodByName, which has a caller context.
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
	if td.Name != "" {
		if td.Pkg != nil && td.Pkg.Path != "" && !strings.HasPrefix(td.Name, td.Pkg.Path+".") {
			return td.Pkg.Path + "." + td.Name
		}
		return td.Name
	}
	if td.Anon != nil {
		if s := runtime.TypSpelling(td.Anon, td); s != "" {
			return "anon:" + s
		}
	}
	if td.Elem != nil {
		switch td.Kind {
		case runtime.KindPointer:
			return "*" + e.keyOf(td.Elem)
		case runtime.KindSlice:
			return "[]" + e.keyOf(td.Elem)
		case runtime.KindChan:
			return "chan " + e.keyOf(td.Elem)
		case runtime.KindMap:
			// map keys ride on td.Anon (or keyTd on RType) — the
			// spelling branch above catches the anon case.
		}
	}
	return fmt.Sprintf("td:%p", td)
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

// displayName renders a td the way reflect.Type.String() would.
func (e *Env) typeName(td *runtime.TypeDef) string {
	if td == nil {
		return "<nil>"
	}
	if td.Name != "" {
		if td.Pkg != nil && td.Pkg.Name != "" {
			if strings.HasPrefix(td.Name, td.Pkg.Path+".") {
				return td.Pkg.Name + "." + td.Name[len(td.Pkg.Path)+1:]
			}
			if i := strings.LastIndex(td.Name, "."); i >= 0 {
				return td.Pkg.Name + td.Name[i:]
			}
			return td.Pkg.Name + "." + td.Name
		}
		if i := strings.LastIndex(td.Name, "."); i >= 0 {
			// bound typedefs name themselves "pkgpath.Name"
			return td.Name[i+1:]
		}
		return td.Name
	}
	if td.Anon != nil {
		return runtime.TypSpelling(td.Anon, td)
	}
	if td.Elem != nil {
		switch td.Kind {
		case runtime.KindPointer:
			return "*" + e.typeName(td.Elem)
		case runtime.KindSlice:
			return "[]" + e.typeName(td.Elem)
		case runtime.KindMap:
			return "map[?]" + e.typeName(td.Elem)
		case runtime.KindChan:
			return "chan " + e.typeName(td.Elem)
		case runtime.KindInterface:
			return "interface {}"
		}
	}
	return "<unnamed>"
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
			if et, err := e.h.ResolveType(td, x); err == nil {
				return et
			}
		}
	}
	return nil
}

// keyTdOf resolves a map td's key type.
func (e *Env) keyTdOf(td *runtime.TypeDef) *runtime.TypeDef {
	if td == nil {
		return nil
	}
	if e.h.ResolveType != nil {
		if mt, ok := td.Anon.(*ast.MapType); ok {
			if kt, err := e.h.ResolveType(td, mt.Key); err == nil {
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

// typeMethods wraps the hook.
func (e *Env) typeMethods(td *runtime.TypeDef) map[string]bool {
	if e.h.TypeMethods == nil || td == nil {
		return nil
	}
	m, err := e.h.TypeMethods(td)
	if err != nil {
		return nil
	}
	return m
}

// ifaceReqs wraps the hook.
func (e *Env) ifaceReqs(td *runtime.TypeDef) map[string]bool {
	if e.h.IfaceReqs == nil || td == nil {
		return nil
	}
	m, err := e.h.IfaceReqs(td)
	if err != nil {
		return nil
	}
	return m
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
	if t.td.Pkg != nil {
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
	return t.e.typeName(t.td)
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
	trap("call of reflect.Type.Elem on type %s", t.String())
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
		trap("call of reflect.Type.Key on type %s", t.String())
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
	trap("call of reflect.Type.Len on type %s", t.String())
	return 0
}

// NumField reports a struct's field count.
func (t *RType) NumField() int {
	if t.rt != nil {
		return t.rt.NumField()
	}
	if t.Kind() != reflect.Struct {
		trap("call of reflect.Type.NumField on type %s", t.String())
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
			Index:     f.Index,
			Anonymous: f.Anonymous,
		}
	}
	if t.Kind() != reflect.Struct {
		trap("call of reflect.Type.Field on type %s", t.String())
	}
	if i < 0 || i >= len(t.td.Fields) {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect: Field index %d out of range", i)})
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
		Index:     []int{i},
		Anonymous: embedded,
	}
	if !embedded && name != "" && !unicode.IsUpper(rune(name[0])) {
		// unexported field: reflect reports the declaring pkg path
		sf.PkgPath = t.PkgPath()
	}
	if t.td.FTags != nil {
		sf.Tag = reflect.StructTag(t.td.FTags[name])
	}
	return sf
}

// FieldByIndex resolves a nested field path.
func (t *RType) FieldByIndex(idx []int) *StructField {
	cur := t
	var f *StructField
	for _, i := range idx {
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
			Type: t.e.hostTypeOf(f.Type), Tag: f.Tag, Index: f.Index,
			Anonymous: f.Anonymous}, true
	}
	if t.Kind() != reflect.Struct {
		trap("call of reflect.Type.FieldByName on type %s", t.String())
	}
	for i, fn := range t.td.Fields {
		if fn == name {
			return t.Field(i), true
		}
	}
	fts := t.e.fieldTypes(t.td)
	for _, ei := range t.td.EmbedIdx {
		if ei < len(fts) && fts[ei] != nil {
			if sub := t.e.rtypeOf(fts[ei]); sub.Kind() == reflect.Struct {
				if f, ok := sub.FieldByName(name); ok {
					f.Index = append([]int{ei}, f.Index...)
					return f, true
				}
			}
		}
	}
	return nil, false
}

// NumMethod reports the method set size.
func (t *RType) NumMethod() int {
	if t.rt != nil {
		return t.rt.NumMethod()
	}
	return len(t.e.typeMethods(t.td))
}

// Method reports the i'th method in sorted order.
func (t *RType) Method(i int) *Method {
	if t.rt != nil {
		m := t.rt.Method(i)
		return &Method{Name: m.Name, PkgPath: m.PkgPath,
			Type: t.e.hostTypeOf(m.Type), Index: m.Index}
	}
	names := sortedKeys(t.e.typeMethods(t.td))
	if i < 0 || i >= len(names) {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect: Method index %d out of range", i)})
	}
	return &Method{Name: names[i], PkgPath: t.PkgPath(), Index: i}
}

// MethodByName looks up a method by name.
func (t *RType) MethodByName(name string) (*Method, bool) {
	if t.rt != nil {
		m, ok := t.rt.MethodByName(name)
		if !ok {
			return nil, false
		}
		return &Method{Name: m.Name, PkgPath: m.PkgPath,
			Type: t.e.hostTypeOf(m.Type), Index: m.Index}, true
	}
	names := sortedKeys(t.e.typeMethods(t.td))
	for i, n := range names {
		if n == name {
			return &Method{Name: n, PkgPath: t.PkgPath(), Index: i}, true
		}
	}
	return nil, false
}

// Implements reports whether the type implements interface u.
func (t *RType) Implements(u *RType) bool {
	if t.rt != nil {
		if u.rt != nil {
			return t.rt.Implements(u.rt)
		}
		return u.scriptImplements(t.hostMethodSet())
	}
	if u.rt != nil {
		return hostIfaceImplemented(u.rt, t.e.typeMethods(t.td))
	}
	if u.td == nil {
		return false
	}
	if len(u.td.MReqs) == 0 && len(u.td.IEmbeds) == 0 {
		return true
	}
	reqs := t.e.ifaceReqs(u.td)
	have := t.e.typeMethods(t.td)
	for m := range reqs {
		if !have[m] {
			return false
		}
	}
	return true
}

// scriptImplements checks a host method set against this script
// interface type.
func (t *RType) scriptImplements(have map[string]bool) bool {
	if t.td == nil {
		return false
	}
	if len(t.td.MReqs) == 0 && len(t.td.IEmbeds) == 0 {
		return true
	}
	for m := range t.e.ifaceReqs(t.td) {
		if !have[m] {
			return false
		}
	}
	return true
}

func (t *RType) hostMethodSet() map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.rt.NumMethod(); i++ {
		out[t.rt.Method(i).Name] = true
	}
	return out
}

// hostIfaceImplemented reports whether the script method set satisfies
// a host interface type.
func hostIfaceImplemented(u reflect.Type, have map[string]bool) bool {
	for i := 0; i < u.NumMethod(); i++ {
		if !have[u.Method(i).Name] {
			return false
		}
	}
	return true
}

// AssignableTo reports assignment compatibility (approximation: equal
// canonical types, or u an interface the type implements).
func (t *RType) AssignableTo(u *RType) bool {
	if u == nil {
		return false
	}
	if t == u || t.key == u.key {
		return true
	}
	if u.Kind() == reflect.Interface {
		return t.Implements(u)
	}
	return false
}

// ConvertibleTo is the loose conversion rule the facade supports.
func (t *RType) ConvertibleTo(u *RType) bool {
	if u == nil {
		return false
	}
	if t == u || t.key == u.key {
		return true
	}
	tk, uk := t.Kind(), u.Kind()
	numeric := func(k reflect.Kind) bool {
		return k >= reflect.Int && k <= reflect.Complex128
	}
	if numeric(tk) && numeric(uk) {
		return true
	}
	if (tk == reflect.String && uk == reflect.Slice) || (tk == reflect.Slice && uk == reflect.String) {
		return true
	}
	return false
}

// Comparable reports whether values of the type can be compared.
func (t *RType) Comparable() bool {
	if t.rt != nil {
		return t.rt.Comparable()
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Map, reflect.Func:
		return false
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
	trap("call of reflect.Type.Bits on type %s", t.String())
	return 0
}

// Align reports the type's alignment — the facade does not model
// machine layout, so script types report 0.
func (t *RType) Align() int {
	if t.rt != nil {
		return t.rt.Align()
	}
	return 0
}

// FieldAlign reports the field alignment.
func (t *RType) FieldAlign() int {
	if t.rt != nil {
		return t.rt.FieldAlign()
	}
	return 0
}

// NumIn reports a func type's input count.
func (t *RType) NumIn() int {
	if t.rt != nil {
		return t.rt.NumIn()
	}
	ft := funcSig(t)
	if ft == nil {
		trap("call of reflect.Type.NumIn on type %s", t.String())
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
		trap("call of reflect.Type.NumOut on type %s", t.String())
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
	x := funcParam(t, i, false)
	if x == nil {
		trap("call of reflect.Type.In on type %s", t.String())
	}
	return t.resolveIn(x)
}

// Out resolves a func type's i'th output type.
func (t *RType) Out(i int) *RType {
	if t.rt != nil {
		return t.e.hostTypeOf(t.rt.Out(i))
	}
	x := funcParam(t, i, true)
	if x == nil {
		trap("call of reflect.Type.Out on type %s", t.String())
	}
	return t.resolveIn(x)
}

// IsVariadic reports whether a func type is variadic.
func (t *RType) IsVariadic() bool {
	if t.rt != nil {
		return t.rt.IsVariadic()
	}
	ft := funcSig(t)
	if ft == nil || ft.Params == nil || len(ft.Params.List) == 0 {
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
	td, err := t.e.h.ResolveType(t.td, x)
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
// counting unnamed entries singly.
func funcParam(t *RType, i int, results bool) ast.Expr {
	ft := funcSig(t)
	if ft == nil {
		return nil
	}
	list := ft.Params
	if results {
		list = ft.Results
	}
	if list == nil {
		return nil
	}
	n := 0
	for _, f := range list.List {
		cnt := len(f.Names)
		if cnt == 0 {
			cnt = 1
		}
		if i < n+cnt {
			return f.Type
		}
		n += cnt
	}
	return nil
}

// sortedKeys sorts a method-name set for deterministic NumMethod/Method.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
