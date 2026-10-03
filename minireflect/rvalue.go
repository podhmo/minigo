package minireflect

import (
	"fmt"
	"go/ast"
	"go/constant"
	"reflect"
	"strings"
	"unicode"

	"github.com/podhmo/minigo/runtime"
)

// RValue is the facade's reflect.Value. In the script domain it views a
// runtime.Value (val) and, when the location is settable, the ref-view
// that produced it (ref: *Cell / *FieldRef / *IndexRef / *DerefRef).
// In the host domain it delegates to a real reflect.Value — host
// objects keep faithful semantics and never cross to script land.
type RValue struct {
	e  *Env
	vc runtime.VMCaller // the VM that produced this value, for Call/Member

	rv  reflect.Value    // host domain; valid iff the value is host-side
	val runtime.Value    // script-domain read view
	ref runtime.Value    // settable location (ref-view), or nil
	td  *runtime.TypeDef // static type of val
	ro  bool             // obtained through an unexported field
}

// host reports whether this value delegates to a real reflect.Value.
func (v *RValue) host() bool { return v != nil && v.rv.IsValid() }

// get reads the current script value, through the ref when settable.
func (v *RValue) get() runtime.Value {
	if v.ref != nil {
		if d, ok := runtime.Deref(v.ref); ok {
			return d
		}
	}
	return v.val
}

// ifaceVal produces the interface{} payload: raw script values pass
// through untouched, host values stay boxed one level shallower.
// Boxing a script value into an interface copies it like a Go
// assignment — structs detach, slices/maps/pointers share — so a
// payload handed to Interface/Append/SetMapIndex/Send cannot alias
// the location it was read from.
func (v *RValue) ifaceVal() any {
	if v.host() {
		return v.rv.Interface()
	}
	return runtime.Copy(v.get())
}

// mustValid traps on a zero/invalid Value like reflect does —
// `reflect: call of reflect.Value.X on zero Value`.
func (v *RValue) mustValid(op string) {
	if !v.IsValid() {
		trap("call of reflect.Value.%s on zero Value", op)
	}
}

// kindStr renders a kind for messages. Go's ValueError spells the
// invalid kind "zero", not its Kind().String() — every `call of
// reflect.Value.X on zero Value` trap goes through here.
func (v *RValue) kindStr() string {
	if k := v.Kind(); k != reflect.Invalid {
		return k.String()
	}
	return "zero"
}

// wrap builds a script-domain rvalue view.
func (e *Env) wrap(vc runtime.VMCaller, val, ref runtime.Value, td *runtime.TypeDef) *RValue {
	return &RValue{e: e, vc: vc, val: val, ref: ref, td: td}
}

// Unwrap exposes the payload a host fmt should print in place of the
// Value itself — mirroring fmt's one-level reflect.Value unwrap, which
// reads through the unexported-field flag. Host-domain values yield
// their interface payload when they can; a non-interfacable host value
// degrades to its scalar accessor or, past those, to the RValue itself
// so at least its `<T Value>` String shows. Script-domain values yield
// the viewed runtime.Value. The payload may itself be a Value: fmt
// renders that one through String (Go's nested `<T Value>` form), it
// does not unwrap twice. Callers gate IsValid themselves.
func (v *RValue) Unwrap() any {
	if v.host() {
		if v.rv.CanInterface() {
			return v.rv.Interface()
		}
		switch v.rv.Kind() {
		case reflect.Bool:
			return v.rv.Bool()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return v.rv.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return v.rv.Uint()
		case reflect.Float32, reflect.Float64:
			return v.rv.Float()
		case reflect.Complex64, reflect.Complex128:
			return v.rv.Complex()
		case reflect.String:
			return v.rv.String()
		}
		return v
	}
	return v.get()
}

// unwrap views a Named value through its underlying value so
// kind-dispatched accessors reach the concrete branch. The ref is
// dropped on purpose: get() prefers it, and dereferencing the same
// location hands the Named tag right back — forwarding it here was an
// infinite recursion (Index/Len/Cap on `ValueOf(&named).Elem()` never
// terminated). Callers keep v.td so kind strings still name the
// declared type; elements of named arrays lose ref addressability as
// a corner case.
func (v *RValue) unwrap() *RValue {
	x, ok := v.get().(*runtime.Named)
	if !ok {
		return v
	}
	w := v.e.wrap(v.vc, x.V, nil, v.td)
	w.ro = v.ro
	return w
}

// wrapHost builds a host-domain rvalue.
func (e *Env) wrapHost(vc runtime.VMCaller, rv reflect.Value) *RValue {
	return &RValue{e: e, vc: vc, rv: rv}
}

// typeOfValue derives a typedef from a dynamic script value, mirroring
// vm.typeOfValue closely enough for the facade.
func typeOfValue(e *Env, v runtime.Value) *runtime.TypeDef {
	switch x := v.(type) {
	case nil, runtime.Nil:
		return nil
	case *runtime.Named:
		if x.Typ != nil {
			return x.Typ
		}
		return typeOfValue(e, x.V)
	case *runtime.TypedNil:
		return x.Typ
	case *runtime.Struct:
		return x.Def
	case *runtime.Slice:
		if x.Typ != nil {
			return x.Typ
		}
		var et *runtime.TypeDef
		if len(x.Elems) > 0 {
			et = typeOfValue(e, x.Elems[0])
		}
		return e.compositeTd(runtime.KindSlice, et)
	case *runtime.Map:
		if x.Typ != nil {
			return x.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindMap}
	case *runtime.Chan:
		if x.Typ != nil {
			return x.Typ
		}
		return &runtime.TypeDef{Kind: runtime.KindChan}
	case *runtime.Cell:
		// the slot's declared type is authoritative: `var v I` keeps the
		// interface typedef even when the slot currently holds nil.
		et := typeOfValue(e, x.Elem)
		if x.Typ != nil {
			et = x.Typ
		}
		return e.pointerTd(et)
	case *runtime.FieldRef:
		// the declared field type comes from the owner's typedef
		var et *runtime.TypeDef
		if s := structOf(x.Base); s != nil && s.Def != nil {
			if fts := e.fieldTypes(s.Def); fts != nil {
				for i, fn := range s.Def.Fields {
					if fn == x.Name && i < len(fts) {
						et = fts[i]
						break
					}
				}
			}
		}
		if et == nil {
			if dv, ok := runtime.Deref(v); ok {
				et = typeOfValue(e, dv)
			}
		}
		return e.pointerTd(et)
	case *runtime.IndexRef:
		var et *runtime.TypeDef
		if s, ok := x.Base.(*runtime.Slice); ok && s.Typ != nil {
			et = e.elemOf(s.Typ)
		}
		if et == nil {
			if dv, ok := runtime.Deref(v); ok {
				et = typeOfValue(e, dv)
			}
		}
		return e.pointerTd(et)
	case *runtime.DerefRef:
		var et *runtime.TypeDef
		if dv, ok := runtime.Deref(v); ok {
			et = typeOfValue(e, dv)
		}
		return e.pointerTd(et)
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return e.funcTd(x)
	case *runtime.GoValue:
		if x.V == nil {
			return nil
		}
		if rv, ok := x.V.(*RValue); ok {
			return rv.staticTd()
		}
		name := reflect.TypeOf(x.V).String()
		if btd := runtime.BasicTypedef(name); btd != nil {
			return btd
		}
		return &runtime.TypeDef{Name: name}
	case int64:
		return runtime.BasicTypedef("int")
	case float64:
		return runtime.BasicTypedef("float64")
	case string:
		return runtime.BasicTypedef("string")
	case bool:
		return runtime.BasicTypedef("bool")
	case *runtime.UConst:
		switch x.V.Kind() {
		case constant.Bool:
			return runtime.BasicTypedef("bool")
		case constant.String:
			return runtime.BasicTypedef("string")
		case constant.Float:
			return runtime.BasicTypedef("float64")
		case constant.Complex:
			return runtime.BasicTypedef("complex128")
		default:
			return runtime.BasicTypedef("int")
		}
	}
	return nil
}

// pointerTd builds a *T typedef carrying its spelling — synthesized
// composites keep an Anon so they intern to the same RType as the
// equivalent declared type (and as a host *T).
func (e *Env) pointerTd(et *runtime.TypeDef) *runtime.TypeDef {
	td := &runtime.TypeDef{Kind: runtime.KindPointer, Elem: et}
	if et != nil {
		// the elem's package context lets display spellings requalify
		// the synthesized path-typed ident back to the declared name.
		td.Pkg, td.File = et.Pkg, et.File
		td.Anon = &ast.StarExpr{X: e.exprOf(et)}
	}
	return td
}

// compositeTd builds an element-composite typedef carrying its spelling
// for the same interning reason as pointerTd. Only slice is needed —
// map and chan without a declared typedef lack the rest of their shape.
func (e *Env) compositeTd(kind runtime.TypeKind, et *runtime.TypeDef) *runtime.TypeDef {
	td := &runtime.TypeDef{Kind: kind, Elem: et}
	if et != nil {
		td.Pkg, td.File = et.Pkg, et.File
		if kind == runtime.KindSlice {
			td.Anon = &ast.ArrayType{Elt: e.exprOf(et)}
		}
	}
	return td
}

// funcTd builds the dynamic typedef of a function value: its identity
// is the signature, spelled through the declaration's FuncType so two
// functions of the same signature intern to the same type.
func (e *Env) funcTd(v runtime.Value) *runtime.TypeDef {
	var decl *ast.FuncDecl
	switch x := v.(type) {
	case *runtime.Function:
		decl = x.Decl
	case *runtime.Closure:
		if x.Fn != nil {
			decl = x.Fn.Decl
		}
	case *runtime.BoundMethod:
		if x.Fn != nil {
			decl = x.Fn.Decl
		}
	}
	td := &runtime.TypeDef{Kind: runtime.KindFunc}
	if decl != nil {
		td.Anon = decl.Type
	}
	return td
}

// staticTd reports the static type of a value (for nested ValueOf).
func (v *RValue) staticTd() *runtime.TypeDef {
	if v.host() {
		return &runtime.TypeDef{Name: v.rv.Type().String()}
	}
	return v.td
}

// structOf unwraps a script value to its *runtime.Struct.
func structOf(v runtime.Value) *runtime.Struct {
	for {
		switch x := v.(type) {
		case *runtime.Struct:
			return x
		case *runtime.Named:
			v = x.V
		default:
			if d, ok := runtime.Deref(v); ok {
				v = d
				continue
			}
			return nil
		}
	}
}

// ---- reflect.Value methods ----

// IsValid reports whether the value is non-zero.
func (v *RValue) IsValid() bool {
	if v == nil {
		return false
	}
	return v.rv.IsValid() || v.val != nil
}

// Kind reports the value's kind.
func (v *RValue) Kind() reflect.Kind {
	if v == nil || !v.IsValid() {
		return reflect.Invalid
	}
	if v.host() {
		return v.rv.Kind()
	}
	if k := v.e.kindOfTd(v.td); k != reflect.Invalid {
		return k
	}
	return kindOfValue(v.get())
}

// kindOfValue derives a kind from the dynamic value.
func kindOfValue(x runtime.Value) reflect.Kind {
	switch v := x.(type) {
	case *runtime.Named:
		return kindOfValue(v.V)
	case int64:
		return reflect.Int
	case float64:
		return reflect.Float64
	case string:
		return reflect.String
	case bool:
		return reflect.Bool
	case *runtime.Slice:
		return reflect.Slice
	case *runtime.Map:
		return reflect.Map
	case *runtime.Struct:
		return reflect.Struct
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		return reflect.Ptr
	case *runtime.Chan:
		return reflect.Chan
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return reflect.Func
	case *runtime.UConst:
		// an untyped constant reads at its default type's kind
		switch v.V.Kind() {
		case constant.Bool:
			return reflect.Bool
		case constant.String:
			return reflect.String
		case constant.Int:
			if v.Rune {
				return reflect.Int32
			}
			return reflect.Int
		case constant.Float:
			return reflect.Float64
		case constant.Complex:
			return reflect.Complex128
		}
		return reflect.Invalid
	case *runtime.GoValue:
		if v.V == nil {
			return reflect.Invalid
		}
		return reflect.ValueOf(v.V).Kind()
	case *runtime.TypedNil:
		if v.Typ != nil {
			switch v.Typ.Kind {
			case runtime.KindPointer:
				return reflect.Ptr
			case runtime.KindSlice:
				return reflect.Slice
			case runtime.KindMap:
				return reflect.Map
			case runtime.KindChan:
				return reflect.Chan
			case runtime.KindFunc:
				return reflect.Func
			case runtime.KindInterface:
				return reflect.Interface
			}
		}
		return reflect.Invalid
	}
	return reflect.Invalid
}

// Type reports the value's type.
func (v *RValue) Type() *RType {
	v.mustValid("Type")
	if v.host() {
		return v.e.hostTypeOf(v.rv.Type())
	}
	td := v.td
	if td == nil {
		td = typeOfValue(v.e, v.get())
	}
	if td == nil {
		trap("minireflect: no type information for %T", v.get())
	}
	return v.e.rtypeOf(td)
}

// Interface returns the value as any.
func (v *RValue) Interface() any {
	v.mustValid("Interface")
	if v.ro {
		plain("reflect.Value.Interface: cannot return value obtained from unexported field or method")
	}
	return v.ifaceVal()
}

// CanInterface reports whether Interface is legal. A zero Value
// panics here like Go — unlike CanAddr/CanSet which answer false.
func (v *RValue) CanInterface() bool {
	v.mustValid("CanInterface")
	return !v.ro
}

// Elem dereferences a pointer or interface value.
func (v *RValue) Elem() *RValue {
	v.mustValid("Elem")
	if v.host() {
		switch v.rv.Kind() {
		case reflect.Ptr, reflect.Interface:
			return v.e.wrapHost(v.vc, v.rv.Elem())
		}
		trap("call of reflect.Value.Elem on %s Value", v.kindStr())
	}
	switch v.Kind() {
	case reflect.Ptr:
		// the pointer object is the cell/ref-view itself
		ptr := unwrapRef(v.get())
		dv, ok := runtime.Deref(ptr)
		if !ok {
			// dereferencing a nil pointer value gives the invalid Value
			return &RValue{e: v.e, vc: v.vc}
		}
		// the pointee is addressable: writes go back through the ptr;
		// read-only provenance survives the deref like Go's flagRO
		return &RValue{e: v.e, vc: v.vc, val: dv, ref: ptr,
			td: v.e.elemOf(v.td), ro: v.ro}
	case reflect.Interface:
		d := v.get()
		if d == nil || d == runtime.NIL {
			return &RValue{e: v.e, vc: v.vc}
		}
		if tn, isNil := d.(*runtime.TypedNil); isNil {
			// an interface holding a typed nil: Elem exposes the typed
			// nil's value like Go's v.Elem() on a non-nil interface
			return &RValue{e: v.e, vc: v.vc, val: tn, td: tn.Typ, ro: v.ro}
		}
		return &RValue{e: v.e, vc: v.vc, val: d,
			td: typeOfValue(v.e, d), ro: v.ro}
	}
	trap("call of reflect.Value.Elem on %s Value", v.kindStr())
	return nil
}

// Addr takes the address of an addressable value.
func (v *RValue) Addr() *RValue {
	v.mustValid("Addr")
	if v.host() {
		if !v.rv.CanAddr() {
			trap("call of reflect.Value.Addr on unaddressable value")
		}
		return v.e.wrapHost(v.vc, v.rv.Addr())
	}
	if v.ref == nil {
		trap("call of reflect.Value.Addr on unaddressable value")
	}
	ptd := &runtime.TypeDef{Kind: runtime.KindPointer, Elem: v.td, Pkg: v.td.Pkg, File: v.td.File}
	// the ref-view object itself IS the pointer value
	return v.e.wrap(v.vc, v.ref, nil, ptd)
}

// CanAddr reports addressability.
func (v *RValue) CanAddr() bool {
	if v.host() {
		return v.rv.CanAddr()
	}
	return v.ref != nil
}

// CanSet reports settability.
func (v *RValue) CanSet() bool {
	if !v.IsValid() {
		return false
	}
	if v.host() {
		return v.rv.CanSet()
	}
	return v.ref != nil && !v.ro
}

// expectKind panics like Go's reflect when the value's kind is not one
// the accessor admits.
func (v *RValue) expectKind(name string, kinds ...reflect.Kind) {
	k := v.Kind()
	for _, kk := range kinds {
		if k == kk {
			return
		}
	}
	plain("reflect: call of reflect.Value.%s on %s Value", name, v.kindStr())
}

// mustBeSettable checks Go's mustBeAssignable — the ro/unaddressable
// gates run before the kind gate in every scalar setter, and the panic
// names the caller's method, not Set.
func (v *RValue) mustBeSettable(name string) {
	if v.ro {
		trap("reflect.Value.%s using value obtained using unexported field", name)
	}
	if v.ref == nil {
		trap("reflect.Value.%s using unaddressable value", name)
	}
}

// IsNil reports nil-ness for nilable kinds.
func (v *RValue) IsNil() bool {
	v.mustValid("IsNil")
	if v.host() {
		switch v.rv.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
			reflect.Ptr, reflect.Slice, reflect.UnsafePointer:
			return v.rv.IsNil()
		}
		trap("call of reflect.Value.IsNil on %s Value", v.kindStr())
	}
	v.expectKind("IsNil", reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Ptr, reflect.Slice, reflect.UnsafePointer)
	// in the cell model a ref-view IS a pointer and is never nil; a nil
	// pointer value arrives as the TypedNil it stores.
	switch x := unwrapRef(v.get()).(type) {
	case nil, runtime.Nil:
		return true
	case *runtime.TypedNil, *runtime.IfaceNil:
		return true
	case *runtime.GoValue:
		return x.V == nil
	}
	return false
}

// IsZero reports whether the value is its type's zero — a non-nil
// slice or map is never zero even when empty, an array is zero only
// when every element is, and the call panics on an invalid Value.
func (v *RValue) IsZero() bool {
	v.mustValid("IsZero")
	if v.host() {
		return v.rv.IsZero()
	}
	x := v.get()
	switch t := x.(type) {
	case nil, runtime.Nil:
		return true
	case *runtime.TypedNil, *runtime.IfaceNil:
		return true
	case *runtime.Named:
		return v.e.wrap(v.vc, t.V, nil, v.td).IsZero()
	case int64:
		return t == 0
	case float64:
		return t == 0
	case string:
		return t == ""
	case bool:
		return !t
	case *runtime.Slice:
		if arrayTypeOf(t.Typ) != nil {
			for i := range t.Elems {
				if !v.Index(i).IsZero() {
					return false
				}
			}
			return true
		}
		return false // a non-nil slice is never the zero value
	case *runtime.Map:
		return false // a non-nil map is never the zero value
	case *runtime.Struct:
		for i := range t.Fields {
			if !v.Field(i).IsZero() {
				return false
			}
		}
		return true
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		return false
	}
	return false
}

// Field reports a struct's i'th field.
func (v *RValue) Field(i int) *RValue {
	v.mustValid("Field")
	if v.host() {
		rv := v.rv
		if rv.Kind() != reflect.Struct {
			// Go panics Field on a ptr — only Elem() first can
			// reach the pointee's fields.
			trap("call of reflect.Value.Field on %s Value", v.kindStr())
		}
		f := rv.Field(i)
		return &RValue{e: v.e, vc: v.vc, rv: f, ro: !f.CanInterface()}
	}
	// Field admits only a struct kind — structOf would happily deref a
	// pointer, but Go panics on *S values.
	if v.Kind() != reflect.Struct {
		trap("call of reflect.Value.Field on %s Value", v.kindStr())
	}
	s := structOf(v.get())
	if s == nil {
		trap("call of reflect.Value.Field on %s Value", v.kindStr())
	}
	if i < 0 || i >= len(s.Fields) {
		panic(&runtime.Panic{Value: "reflect: Field index out of range"})
	}
	def := s.Def
	var ftd *runtime.TypeDef
	if def != nil {
		if fts := v.e.fieldTypes(def); i < len(fts) {
			ftd = fts[i]
		}
	}
	name := ""
	if def != nil && i < len(def.Fields) {
		name = def.Fields[i]
	}
	ro := v.ro || (name != "" && !isExported(name))
	var ref runtime.Value
	if v.ref != nil {
		// the field's location is the parent's, not this struct object:
		// a later Set through v replaces the storage wholesale (struct
		// assignment overwrites the same memory in Go), and a ref pinned
		// to the old object would write into a detached copy. An
		// unaddressable parent yields an unaddressable field — Go only
		// inherits addressability through Elem() of a pointer.
		ref = &runtime.FieldRef{Base: v.ref, Name: name}
	}
	return &RValue{
		e: v.e, vc: v.vc,
		val: s.Fields[i],
		ref: ref,
		td:  ftd,
		ro:  ro,
	}
}

// NumField reports a struct value's field count.
func (v *RValue) NumField() int {
	v.mustValid("NumField")
	if v.host() {
		// like Field, NumField must be a struct kind directly —
		// rv.NumField fires Go's own kind panic.
		return v.rv.NumField()
	}
	// struct-of-pointers do not deref here — Go's NumField must be a
	// struct kind directly.
	if v.Kind() != reflect.Struct {
		trap("call of reflect.Value.NumField on %s Value", v.kindStr())
	}
	if s := structOf(v.get()); s != nil {
		return len(s.Fields)
	}
	trap("call of reflect.Value.NumField on %s Value", v.kindStr())
	return 0
}

// FieldByIndex resolves a nested field path.
func (v *RValue) FieldByIndex(idx []int) *RValue {
	if v.host() {
		f := v.rv.FieldByIndex(idx)
		ro := false
		if f.IsValid() {
			ro = !f.CanInterface()
		}
		return &RValue{e: v.e, vc: v.vc, rv: f, ro: ro}
	}
	cur := v
	for depth, i := range idx {
		// embedded traversal derefs a ptr-to-struct field between
		// steps; a nil embedded pointer dies on 'indirection through
		// nil pointer to embedded struct' like Go's FieldByIndexErr.
		if depth > 0 && cur.Kind() == reflect.Ptr {
			if cur.Type() != nil && cur.Type().Elem() != nil && cur.Type().Elem().Kind() == reflect.Struct {
				ev := cur.Elem()
				if !ev.IsValid() {
					panic(&runtime.Panic{Value: "reflect: indirection through nil pointer to embedded struct"})
				}
				cur = ev
			}
		}
		cur = cur.Field(i)
	}
	return cur
}

// FieldByName looks up a struct field by name, including promotion
// through embedded fields (value or pointer).
func (v *RValue) FieldByName(name string) *RValue {
	v.mustValid("FieldByName")
	if v.host() {
		// no deref here either — FieldByName on a ptr dies on
		// 'call of reflect.Value.FieldByName on ptr Value'.
		f := v.rv.FieldByName(name)
		// a miss yields the zero Value — ro means nothing there and
		// CanInterface on a zero Value itself panics; only a live field
		// can carry the unexported flag.
		ro := false
		if f.IsValid() {
			ro = !f.CanInterface()
		}
		return &RValue{e: v.e, vc: v.vc, rv: f, ro: ro}
	}
	if v.Kind() != reflect.Struct {
		trap("call of reflect.Value.FieldByName on %s Value", v.kindStr())
	}
	s := structOf(v.get())
	if s == nil {
		trap("call of reflect.Value.FieldByName on %s Value", v.kindStr())
	}
	for i, fn := range s.Def.Fields {
		if fn == name {
			return v.Field(i)
		}
	}
	for _, ei := range s.Def.EmbedIdx {
		sub := v.Field(ei)
		for {
			if st := structOf(sub.get()); st != nil {
				if r := sub.fieldByNameRec(st, name); r.IsValid() {
					return r
				}
				break
			}
			ev := sub.Elem()
			if !ev.IsValid() {
				break
			}
			sub = ev
		}
	}
	return &RValue{e: v.e, vc: v.vc}
}

// fieldByNameRec resolves name on the struct value's own fields, then
// descends into embedded fields — the promotion walk of FieldByName.
func (v *RValue) fieldByNameRec(s *runtime.Struct, name string) *RValue {
	for i, fn := range s.Def.Fields {
		if fn == name {
			return v.Field(i)
		}
	}
	for _, ei := range s.Def.EmbedIdx {
		sub := v.Field(ei)
		for {
			if st := structOf(sub.get()); st != nil {
				if r := sub.fieldByNameRec(st, name); r.IsValid() {
					return r
				}
				break
			}
			ev := sub.Elem()
			if !ev.IsValid() {
				break
			}
			sub = ev
		}
	}
	return &RValue{e: v.e, vc: v.vc}
}

// Len reports len() of a slice/array/string/map/chan value.
func (v *RValue) Len() int {
	v.mustValid("Len")
	if v.host() {
		return v.rv.Len()
	}
	switch x := v.get().(type) {
	case *runtime.Slice:
		return len(x.Elems)
	case *runtime.Map:
		return x.Len()
	case string:
		return len(x)
	case *runtime.Chan:
		return len(x.C)
	case *runtime.Named:
		return v.unwrap().Len()
	case runtime.Nil, *runtime.TypedNil:
		// a nil slice/map/chan reports 0 like Go
		switch v.Kind() {
		case reflect.Slice, reflect.Map, reflect.Chan:
			return 0
		}
	}
	trap("call of reflect.Value.Len on %s Value", v.kindStr())
	return 0
}

// SetLen reslices the target slice's header in place like
// reflect.Value.SetLen: shrinking shares the backing, growing past
// capacity panics. Script slices carry cap in their Go backing.
func (v *RValue) SetLen(n int) {
	if v.host() {
		v.rv.SetLen(n)
		return
	}
	v.mustValid("SetLen")
	v.mustBeSettable("SetLen")
	if n2, isN := v.get().(*runtime.Named); isN {
		if s, ok := n2.V.(*runtime.Slice); ok {
			if n >= 0 && n <= cap(s.Elems) {
				// keep the named tag on the resliced header
				v.set(&runtime.Named{Typ: n2.Typ, V: &runtime.Slice{Elems: s.Elems[:n], Typ: s.Typ}})
				return
			}
		}
	}
	s, ok := v.get().(*runtime.Slice)
	if !ok {
		if tn, isNil := v.get().(*runtime.TypedNil); isNil && v.Kind() == reflect.Slice {
			s = &runtime.Slice{Elems: nil, Typ: tn.Typ}
			ok = true
		}
		if !ok {
			trap("call of reflect.Value.SetLen on %s Value", v.kindStr())
		}
	}
	if n < 0 || n > cap(s.Elems) {
		trap("slice length out of range in SetLen")
	}
	v.set(&runtime.Slice{Elems: s.Elems[:n], Typ: s.Typ})
}

// SetCap reslices the target slice's header in place like
// reflect.Value.SetCap: only the capacity changes — the length and the
// backing stay — so n must lie in [len, cap].
func (v *RValue) SetCap(n int) {
	if v.host() {
		v.rv.SetCap(n)
		return
	}
	v.mustValid("SetCap")
	v.mustBeSettable("SetCap")
	if n2, isN := v.get().(*runtime.Named); isN {
		if s, ok := n2.V.(*runtime.Slice); ok {
			if n >= len(s.Elems) && n <= cap(s.Elems) {
				v.set(&runtime.Named{Typ: n2.Typ, V: &runtime.Slice{Elems: s.Elems[:len(s.Elems):n], Typ: s.Typ}})
				return
			}
			trap("slice capacity out of range in SetCap")
		}
	}
	s, ok := v.get().(*runtime.Slice)
	if !ok {
		if tn, isNil := v.get().(*runtime.TypedNil); isNil && v.Kind() == reflect.Slice {
			s = &runtime.Slice{Elems: nil, Typ: tn.Typ}
			ok = true
		}
		if !ok {
			trap("call of reflect.Value.SetCap on %s Value", v.kindStr())
		}
	}
	if n < len(s.Elems) || n > cap(s.Elems) {
		trap("slice capacity out of range in SetCap")
	}
	v.set(&runtime.Slice{Elems: s.Elems[:len(s.Elems):n], Typ: s.Typ})
}

// Cap reports cap() of a slice/array/chan value.
func (v *RValue) Cap() int {
	v.mustValid("Cap")
	if v.host() {
		return v.rv.Cap()
	}
	switch x := v.get().(type) {
	case *runtime.Slice:
		return cap(x.Elems)
	case *runtime.Chan:
		return cap(x.C)
	case *runtime.Named:
		return v.unwrap().Cap()
	case runtime.Nil, *runtime.TypedNil:
		switch v.Kind() {
		case reflect.Slice, reflect.Chan:
			return 0
		}
	}
	trap("call of reflect.Value.Cap on %s Value", v.kindStr())
	return 0
}

// Index reports a slice/array/string's i'th element.
func (v *RValue) Index(i int) *RValue {
	v.mustValid("Index")
	if v.host() {
		if v.rv.Kind() == reflect.String {
			return &RValue{e: v.e, vc: v.vc, rv: v.rv.Index(i)}
		}
		rv := v.rv.Index(i)
		return &RValue{e: v.e, vc: v.vc, rv: rv, ro: v.ro || !rv.CanInterface()}
	}
	var etd *runtime.TypeDef
	if v.td != nil {
		etd = v.e.elemOf(v.td)
	}
	switch x := v.get().(type) {
	case *runtime.Slice:
		if i < 0 || i >= len(x.Elems) {
			if arrayTypeOf(x.Typ) != nil {
				panic(&runtime.Panic{Value: "reflect: array index out of range"})
			}
			panic(&runtime.Panic{Value: "reflect: slice index out of range"})
		}
		var ref runtime.Value
		if arrayTypeOf(x.Typ) != nil {
			// array elements are addressable only when the array is, and
			// they track the parent's location — array assignment
			// overwrites the same storage in Go.
			if v.ref != nil {
				ref = &runtime.IndexRef{Base: v.ref, Key: int64(i)}
			}
		} else {
			// slice elements are always addressable — they live in the
			// shared backing captured here, so a later reassignment of
			// the slice variable does not move the element's view.
			ref = &runtime.IndexRef{Base: x, Key: int64(i)}
		}
		return &RValue{e: v.e, vc: v.vc, val: x.Elems[i],
			ref: ref, td: etd, ro: v.ro}
	case string:
		if i < 0 || i >= len(x) {
			panic(&runtime.Panic{Value: "reflect: string index out of range"})
		}
		// the element is a byte — tag it so %T reads uint8 and
		// Interface() surfaces a typed byte, like a []uint8 element.
		btd := runtime.BasicTypedef("uint8")
		return &RValue{e: v.e, vc: v.vc,
			val: runtime.Tag(btd, int64(x[i])), td: btd}
	case *runtime.Named:
		return v.unwrap().Index(i)
	case *runtime.TypedNil:
		if v.Kind() == reflect.Slice {
			panic(&runtime.Panic{Value: "reflect: slice index out of range"})
		}
	}
	trap("call of reflect.Value.Index on %s Value", v.kindStr())
	return nil
}

// Slice produces a subslice view sharing the backing store.
func (v *RValue) Slice(i, j int) *RValue {
	v.mustValid("Slice")
	if v.host() {
		return v.e.wrapHost(v.vc, v.rv.Slice(i, j))
	}
	switch s := v.get().(type) {
	case string:
		if i < 0 || j > len(s) || i > j {
			plain("reflect.Value.Slice: string slice index out of bounds")
		}
		// Go keeps flagAddr on a sliced string: CanSet reports true
		// and Set writes the view's own header, leaving the parent
		// untouched — a detached cell models exactly that. (ref is an
		// interface: a nil *Cell would deref through get().)
		var ref runtime.Value
		if v.ref != nil {
			ref = &runtime.Cell{Elem: s[i:j]}
		}
		return &RValue{e: v.e, vc: v.vc, val: s[i:j], ref: ref, td: v.td, ro: v.ro}
	case *runtime.Slice:
		// an unaddressable array rejects Slice before the bounds are
		// ever looked at — Go checks addressability first.
		if at := arrayTypeOf(s.Typ); at != nil && v.ref == nil {
			plain("reflect.Value.Slice: slice of unaddressable array")
		}
		// Go bounds a reslice by capacity, not length — s[:1] can
		// grow back to cap(s).
		if i < 0 || j > cap(s.Elems) || i > j {
			plain("reflect.Value.Slice: slice index out of bounds")
		}
		if at := arrayTypeOf(s.Typ); at != nil {
			// slicing an array borrows its storage, so the array must
			// be addressable — and the result is a slice type, not the
			// array's. The Anon keeps the []T spelling so the produced
			// type interns to the same RType as a script []T literal.
			st := &runtime.TypeDef{Kind: runtime.KindSlice, Elem: v.e.elemOf(s.Typ),
				Anon: &ast.ArrayType{Elt: at.Elt}}
			return &RValue{e: v.e, vc: v.vc,
				val: &runtime.Slice{Elems: s.Elems[i:j], Typ: st}, td: st, ro: v.ro}
		}
		return &RValue{e: v.e, vc: v.vc,
			val: &runtime.Slice{Elems: s.Elems[i:j], Typ: s.Typ}, td: v.td, ro: v.ro}
	case *runtime.Named:
		// named string/slice values view through the underlying like
		// every other kind-dispatched accessor. An addressable named
		// keeps settability the same way the string arm does — the
		// cell unwraps to the underlying value, never the Named
		// itself (a Named-typed ref would recurse back here).
		var ref runtime.Value
		if v.ref != nil {
			ref = &runtime.Cell{Elem: s.V}
		}
		nv := &RValue{e: v.e, vc: v.vc, val: s.V, ref: ref, td: v.td, ro: v.ro}
		return nv.Slice(i, j)
	case *runtime.TypedNil:
		if v.Kind() == reflect.Slice {
			if i == 0 && j == 0 {
				// a nil slice reslices to itself — s[:0] stays nil.
				return &RValue{e: v.e, vc: v.vc, val: s, td: v.td, ro: v.ro}
			}
			plain("reflect.Value.Slice: slice index out of bounds")
		}
	}
	trap("call of reflect.Value.Slice on %s Value", v.kindStr())
	return nil
}

// MapIndex looks up a map value; missing keys give an invalid Value.
func (v *RValue) MapIndex(k *RValue) *RValue {
	v.mustValid("MapIndex")
	if v.host() {
		// Go judges the map kind before marshalling the key — let
		// MapIndex raise its own 'call of reflect.Value.MapIndex on
		// X Value' instead of Type().Key()'s 'non-map type' panic.
		if v.rv.Kind() != reflect.Map {
			v.rv.MapIndex(v.rv)
		}
		kr, err := toHost(k.ifaceVal(), v.rv.Type().Key())
		if err != nil {
			trap("reflect.Value.MapIndex: %s", err)
		}
		return &RValue{e: v.e, vc: v.vc, rv: v.rv.MapIndex(kr)}
	}
	// the key must be assignable to the map's declared key type — Go
	// reports `reflect.Value.MapIndex: value of type X is not
	// assignable to type Y` instead of a silent miss.
	if ktd := v.e.keyTdOf(v.td); ktd != nil {
		kt, xt := v.e.rtypeOf(ktd), k.Type()
		if xt != nil && !xt.AssignableTo(kt) {
			plain("reflect.Value.MapIndex: value of type %s is not assignable to type %s", xt.String(), kt.String())
		}
	}
	m, ok := v.get().(*runtime.Map)
	if !ok {
		if _, isNil := v.get().(*runtime.TypedNil); isNil && v.Kind() == reflect.Map {
			// a nil map reads as empty: every key misses
			return &RValue{e: v.e, vc: v.vc}
		}
		if v.get() == runtime.NIL && v.Kind() == reflect.Map {
			return &RValue{e: v.e, vc: v.vc}
		}
		trap("call of reflect.Value.MapIndex on %s Value", v.kindStr())
	}
	var etd *runtime.TypeDef
	if v.td != nil {
		etd = v.e.elemOf(v.td)
	}
	// map values are copies in Go — the read detaches the stored value
	// and there is no ref-view, matching minigo's own map model
	// (IndexRef on a map writes nothing useful).
	if got, ok := m.Get(normVal(k.ifaceVal())); ok {
		return v.e.wrap(v.vc, runtime.Copy(got), nil, etd)
	}
	return &RValue{e: v.e, vc: v.vc}
}

// MapKeys reports the map's keys.
func (v *RValue) MapKeys() []*RValue {
	v.mustValid("MapKeys")
	if v.host() {
		var out []*RValue
		for _, k := range v.rv.MapKeys() {
			out = append(out, v.e.wrapHost(v.vc, k))
		}
		return out
	}
	m, ok := v.get().(*runtime.Map)
	if !ok {
		switch v.get().(type) {
		case runtime.Nil, *runtime.TypedNil:
			if v.Kind() == reflect.Map {
				return nil
			}
		}
		trap("call of reflect.Value.MapKeys on %s Value", v.kindStr())
	}
	var ktd *runtime.TypeDef
	if v.td != nil {
		ktd = v.e.keyTdOf(v.td)
	}
	out := make([]*RValue, 0, m.Len())
	for _, k := range m.Order {
		out = append(out, v.e.wrap(v.vc, k, nil, ktd))
	}
	return out
}

// SetMapIndex assigns or deletes a map entry.
func (v *RValue) SetMapIndex(k, x *RValue) {
	v.mustValid("SetMapIndex")
	if v.host() {
		// same order as MapIndex: the kind check precedes the
		// key/value marshal.
		if v.rv.Kind() != reflect.Map {
			v.rv.SetMapIndex(v.rv, v.rv)
		}
		kr, err := toHost(k.ifaceVal(), v.rv.Type().Key())
		if err != nil {
			trap("reflect.Value.SetMapIndex: %s", err)
		}
		if !x.IsValid() {
			v.rv.SetMapIndex(kr, reflect.Value{})
			return
		}
		xr, err := toHost(x.ifaceVal(), v.rv.Type().Elem())
		if err != nil {
			trap("reflect.Value.SetMapIndex: %s", err)
		}
		v.rv.SetMapIndex(kr, xr)
		return
	}
	m, ok := v.get().(*runtime.Map)
	nilMap := false
	if !ok {
		_, isNil := v.get().(*runtime.TypedNil)
		nilMap = (isNil || v.get() == runtime.NIL) && v.Kind() == reflect.Map
		if !nilMap {
			trap("call of reflect.Value.SetMapIndex on %s Value", v.kindStr())
		}
	}
	// key and value must be assignable to the map's declared types, like
	// Go's `reflect.Value.SetMapIndex: value of type string is not
	// assignable to type int` (no `reflect:` prefix) — and that check
	// still rules on a nil map: Go judges it before touching the
	// backing, while a delete there is a no-op.
	if ktd := v.e.keyTdOf(v.td); ktd != nil {
		kt, xt := v.e.rtypeOf(ktd), k.Type()
		if xt != nil && !xt.AssignableTo(kt) {
			plain("reflect.Value.SetMapIndex: value of type %s is not assignable to type %s", xt.String(), kt.String())
		}
	}
	if !x.IsValid() {
		if m != nil {
			m.Delete(normVal(k.ifaceVal()))
		}
		return
	}
	if etd := v.e.elemOf(v.td); etd != nil {
		et, xt := v.e.rtypeOf(etd), x.Type()
		if xt != nil && !xt.AssignableTo(et) {
			plain("reflect.Value.SetMapIndex: value of type %s is not assignable to type %s", xt.String(), et.String())
		}
	}
	if nilMap {
		plain("assignment to entry in nil map")
	}
	m.Insert(normVal(k.ifaceVal()), normVal(x.ifaceVal()))
}

// MapRange starts a map iteration.
func (v *RValue) MapRange() *MapIter {
	v.mustValid("MapRange")
	if v.host() {
		return &MapIter{e: v.e, vc: v.vc, hit: v.rv.MapRange()}
	}
	m, ok := v.get().(*runtime.Map)
	if !ok {
		trap("call of reflect.Value.MapRange on %s Value", v.kindStr())
	}
	keys := append([]runtime.Value{}, m.Order...)
	var ktd, etd *runtime.TypeDef
	if v.td != nil {
		ktd = v.e.keyTdOf(v.td)
		etd = v.e.elemOf(v.td)
	}
	return &MapIter{e: v.e, vc: v.vc, m: m, keys: keys, ktd: ktd, etd: etd}
}

// MapIter iterates a map.
type MapIter struct {
	e    *Env
	vc   runtime.VMCaller
	m    *runtime.Map
	keys []runtime.Value
	i    int
	ktd  *runtime.TypeDef
	etd  *runtime.TypeDef
	hit  *reflect.MapIter
}

// Next advances the iterator. A key deleted mid-iteration is skipped,
// like Go's map iterator (keys added during iteration may not appear —
// Go leaves that unspecified and the snapshot keeps them out).
func (it *MapIter) Next() bool {
	if it.hit != nil {
		return it.hit.Next()
	}
	for it.i+1 <= len(it.keys) {
		it.i++
		if _, ok := it.m.Get(it.keys[it.i-1]); ok {
			return true
		}
	}
	it.i++ // past the end — Key/Value must trap like Go
	return false
}

// Key reports the current key.
func (it *MapIter) Key() *RValue {
	if it.hit != nil {
		return it.e.wrapHost(it.vc, it.hit.Key())
	}
	if it.i == 0 || it.i > len(it.keys) {
		trap("call of MapIter.Key before Next")
	}
	return it.e.wrap(it.vc, it.keys[it.i-1], nil, it.ktd)
}

// Value reports the current value.
func (it *MapIter) Value() *RValue {
	if it.hit != nil {
		return it.e.wrapHost(it.vc, it.hit.Value())
	}
	if it.i == 0 || it.i > len(it.keys) {
		trap("call of MapIter.Value before Next")
	}
	got, _ := it.m.Get(it.keys[it.i-1])
	return it.e.wrap(it.vc, runtime.Copy(got), nil, it.etd)
}

// Reset restarts the iterator on v.
func (it *MapIter) Reset(v *RValue) {
	if it.hit != nil {
		it.hit.Reset(v.rv)
		return
	}
	nv := v.MapRange()
	*it = *nv
}

// SetIterKey assigns the current iteration key. Go evaluates the
// iterator first — before Next the panic is `reflect: Value.SetIterKey
// called before Next`, ahead of the target's settable check — and an
// unassignable key reports under `reflect.MapIter.SetKey:`.
func (v *RValue) SetIterKey(it *MapIter) {
	if it != nil && it.hit == nil && (it.i == 0 || it.i > len(it.keys)) {
		plain("reflect: Value.SetIterKey called before Next")
	}
	x := it.Key()
	v.mustValid("SetIterKey")
	v.mustBeSettable("SetIterKey")
	if vt, xt := v.Type(), x.Type(); vt != nil && xt != nil && !xt.AssignableTo(vt) {
		plain("reflect.MapIter.SetKey: value of type %s is not assignable to type %s", xt.String(), vt.String())
	}
	v.set(x.get())
}

// SetIterValue assigns the current iteration value.
func (v *RValue) SetIterValue(it *MapIter) {
	if it != nil && it.hit == nil && (it.i == 0 || it.i > len(it.keys)) {
		plain("reflect: Value.SetIterValue called before Next")
	}
	x := it.Value()
	v.mustValid("SetIterValue")
	v.mustBeSettable("SetIterValue")
	if vt, xt := v.Type(), x.Type(); vt != nil && xt != nil && !xt.AssignableTo(vt) {
		plain("reflect.MapIter.SetValue: value of type %s is not assignable to type %s", xt.String(), vt.String())
	}
	v.set(x.get())
}

// ---- scalar reads ----

// Int reads an integer value.
func (v *RValue) Int() int64 {
	v.mustValid("Int")
	if v.host() {
		return v.rv.Int()
	}
	v.expectKind("Int", reflect.Int, reflect.Int8, reflect.Int16,
		reflect.Int32, reflect.Int64)
	switch x := v.get().(type) {
	case int64:
		return x
	case *runtime.Named:
		return intOf(x.V)
	case *runtime.UConst:
		return intOf(x)
	case *runtime.GoValue:
		if rv := reflect.ValueOf(x.V); rv.IsValid() && rv.Kind() >= reflect.Int && rv.Kind() <= reflect.Int64 {
			return rv.Int()
		}
	}
	trap("call of reflect.Value.Int on %s Value", v.kindStr())
	return 0
}

// Uint reads an unsigned value.
func (v *RValue) Uint() uint64 {
	v.mustValid("Uint")
	if v.host() {
		return v.rv.Uint()
	}
	v.expectKind("Uint", reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64, reflect.Uintptr)
	switch x := v.get().(type) {
	case int64:
		return uint64(x)
	case *runtime.Named:
		return uint64(intOf(x.V))
	case *runtime.GoValue:
		if rv := reflect.ValueOf(x.V); rv.IsValid() && rv.Kind() >= reflect.Uint && rv.Kind() <= reflect.Uintptr {
			return rv.Uint()
		}
	}
	trap("call of reflect.Value.Uint on %s Value", v.kindStr())
	return 0
}

// Float reads a float value.
func (v *RValue) Float() float64 {
	v.mustValid("Float")
	if v.host() {
		return v.rv.Float()
	}
	v.expectKind("Float", reflect.Float32, reflect.Float64)
	switch x := v.get().(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case *runtime.Named:
		if f, ok := runtime.Unwrap(x).(float64); ok {
			return f
		}
		return float64(intOf(x.V))
	case *runtime.GoValue:
		if rv := reflect.ValueOf(x.V); rv.IsValid() && (rv.Kind() == reflect.Float32 || rv.Kind() == reflect.Float64) {
			return rv.Float()
		}
	}
	trap("call of reflect.Value.Float on %s Value", v.kindStr())
	return 0
}

// Bool reads a bool value.
func (v *RValue) Bool() bool {
	v.mustValid("Bool")
	if v.host() {
		return v.rv.Bool()
	}
	switch x := v.get().(type) {
	case bool:
		return x
	case *runtime.Named:
		if b, ok := runtime.Unwrap(x).(bool); ok {
			return b
		}
	case *runtime.UConst:
		if x.V.Kind() == constant.Bool {
			return constant.BoolVal(x.V)
		}
	}
	trap("call of reflect.Value.Bool on %s Value", v.kindStr())
	return false
}

// Bytes reads a []byte value. A script slice shares its backing —
// the returned value aliases the same elements like Go's Bytes, so
// writes through it reach the original slice (an unexported field's
// slice included: Go's Bytes ignores the read-only flag).
func (v *RValue) Bytes() any {
	v.mustValid("Bytes")
	if v.host() {
		return v.rv.Bytes()
	}
	// Go dispatches on the kind, not the payload's shape: slice and
	// array must carry a byte element (a named elem whose kind is
	// still uint8 passes), anything else is a bad call — so a nil
	// non-byte slice reports "non-byte slice" like any other.
	kind := v.Kind()
	if kind != reflect.Slice && kind != reflect.Array {
		trap("call of reflect.Value.Bytes on %s Value", v.kindStr())
		return nil
	}
	var s *runtime.Slice
	switch x := v.get().(type) {
	case *runtime.Slice:
		s = x
	case *runtime.Named:
		s, _ = x.V.(*runtime.Slice)
	}
	if s == nil { // a zero/nil Value of slice type (reflect.Zero)
		s = &runtime.Slice{Typ: v.td}
	}
	et := v.e.elemOf(v.td)
	if et == nil {
		et = v.e.elemOf(s.Typ)
	}
	if et != nil && v.e.kindOfTd(et) != reflect.Uint8 {
		if kind == reflect.Array {
			plain("reflect.Value.Bytes of non-byte array")
		}
		plain("reflect.Value.Bytes of non-byte slice")
	}
	if kind == reflect.Array && !v.CanAddr() {
		plain("reflect.Value.Bytes of unaddressable byte array")
	}
	st := s.Typ
	if st == nil {
		st = v.e.compositeTd(runtime.KindSlice, &runtime.TypeDef{Name: "byte"})
	}
	return &runtime.Slice{Elems: s.Elems, Typ: st}
}

// String reads a string value; on non-string kinds it reports the
// "<T Value>" placeholder like reflect.
func (v *RValue) String() string {
	if v == nil || !v.IsValid() {
		return "<invalid Value>"
	}
	if v.host() {
		return v.rv.String()
	}
	if s, ok := v.get().(string); ok {
		return s
	}
	if n, ok := v.get().(*runtime.Named); ok {
		if s, ok := n.V.(string); ok {
			return s
		}
	}
	return fmt.Sprintf("<%s Value>", v.Type().String())
}

// Complex reads a complex value.
func (v *RValue) Complex() complex128 {
	v.mustValid("Complex")
	if v.host() {
		return v.rv.Complex()
	}
	if gv, ok := v.get().(*runtime.GoValue); ok {
		if rv := reflect.ValueOf(gv.V); rv.IsValid() && (rv.Kind() == reflect.Complex64 || rv.Kind() == reflect.Complex128) {
			return rv.Complex()
		}
	}
	trap("call of reflect.Value.Complex on %s Value", v.kindStr())
	return 0
}

// ---- writes ----

// set stores a script value through the ref-view.
func (v *RValue) set(val runtime.Value) {
	if v.ro {
		trap("reflect.Value.Set using value obtained using unexported field")
	}
	if v.ref == nil {
		trap("reflect.Value.Set using unaddressable value")
	}
	// struct assignment is a copy in Go
	if vc := v.vc; vc != nil {
		if _, isStruct := val.(*runtime.Struct); isStruct {
			if cp := vc.Copy(val); cp != nil {
				val = cp
			}
		}
	}
	if !runtime.SetRef(v.ref, v.tagged(val)) {
		trap("reflect.Value.Set: cannot assign to %s", v.kindStr())
	}
	v.val = val
}

// tagged restores a declared named-basic tag on stored scalars.
func (v *RValue) tagged(val runtime.Value) runtime.Value {
	td := v.td
	if td == nil || td.Name == "" || td.Name == "error" {
		return val
	}
	switch td.Kind {
	case runtime.KindNamedBasic:
		if _, is := val.(*runtime.Named); !is && val != nil && val != runtime.NIL {
			return &runtime.Named{Typ: td, V: val}
		}
	}
	return val
}

// Set assigns another value's content — the source must be assignable
// to the target's declared type, like `reflect.Set: value of type
// string is not assignable to type int`.
func (v *RValue) Set(x *RValue) {
	v.mustValid("Set")
	if v.host() {
		// Go judges settability before marshalling the source — an
		// unaddressable target dies on 'using unaddressable value'
		// even when the source would not marshal. Let rv.Set deliver
		// that panic itself.
		if !v.rv.CanSet() {
			v.rv.Set(v.rv)
		}
		rv, err := toHost(x.ifaceVal(), v.rv.Type())
		if err != nil {
			trap("reflect.Value.Set: %s", err)
		}
		v.rv.Set(rv)
		return
	}
	// Go's order: the target must be settable, then the source must be
	// a usable Value (`call of reflect.Value.Set on zero Value`), and
	// only then is its assignability judged.
	v.mustBeSettable("Set")
	if x == nil || !x.IsValid() {
		trap("call of reflect.Value.Set on zero Value")
	}
	if vt, xt := v.Type(), x.Type(); vt != nil && xt != nil && !xt.AssignableTo(vt) {
		plain("reflect.Set: value of type %s is not assignable to type %s", xt.String(), vt.String())
	}
	val := x.get()
	if x.host() {
		val = &runtime.GoValue{V: x.rv.Interface()}
	}
	v.set(val)
}

// SetBool writes a bool.
func (v *RValue) SetBool(b bool) {
	v.mustValid("SetBool")
	if v.host() {
		v.rv.SetBool(b)
		return
	}
	v.mustBeSettable("SetBool")
	v.expectKind("SetBool", reflect.Bool)
	v.set(b)
}

// intWidths maps declared integer type names to their storage width —
// the width SetInt/SetUint truncate to, like Go's reflect setters.
var intWidths = map[string]struct {
	bits   int
	signed bool
}{
	"int": {64, true}, "int8": {8, true}, "int16": {16, true},
	"int32": {32, true}, "int64": {64, true}, "rune": {32, true},
	"uint": {64, false}, "uint8": {8, false}, "uint16": {16, false},
	"uint32": {32, false}, "uint64": {64, false}, "uintptr": {64, false},
	"byte": {8, false},
}

// declTd resolves the type the location was declared as — the slot's
// own typedef, a field's declared type through the owning struct, or a
// slice/array element typedef — used to answer "what width does this
// store truncate to".
func (v *RValue) declTd() *runtime.TypeDef {
	if v.ref == nil {
		return v.td
	}
	switch x := v.ref.(type) {
	case *runtime.Cell:
		if x.Typ != nil {
			return x.Typ
		}
	case *runtime.FieldRef:
		if s := structOf(x.Base); s != nil && s.Def != nil {
			if fts := v.e.fieldTypes(s.Def); fts != nil {
				for i, fn := range s.Def.Fields {
					if fn == x.Name && i < len(fts) {
						return fts[i]
					}
				}
			}
		}
	case *runtime.IndexRef:
		if s := x.Slice(); s != nil && s.Typ != nil {
			return v.e.elemOf(s.Typ)
		}
	}
	if v.td != nil {
		return v.td
	}
	if dv, ok := runtime.Deref(v.ref); ok {
		return typeOfValue(v.e, dv)
	}
	return nil
}

// truncInt masks/sign-extends x to the declared width — SetInt on an
// int8 cell stores int8(257) == 1, never a panic and never 257.
func truncInt(td *runtime.TypeDef, x int64) int64 {
	if td == nil || td.Name == "" {
		return x
	}
	w, ok := intWidths[td.Name]
	if !ok || w.bits >= 64 {
		return x
	}
	if w.signed {
		return x << (64 - w.bits) >> (64 - w.bits)
	}
	return x & ((1 << w.bits) - 1)
}

// SetInt writes an int64, truncated to the declared width.
func (v *RValue) SetInt(x int64) {
	v.mustValid("SetInt")
	if v.host() {
		v.rv.SetInt(x)
		return
	}
	v.mustBeSettable("SetInt")
	v.expectKind("SetInt", reflect.Int, reflect.Int8, reflect.Int16,
		reflect.Int32, reflect.Int64)
	v.set(truncInt(v.declTd(), x))
}

// SetUint writes a uint64 (kept as int64 in the script domain),
// truncated to the declared width.
func (v *RValue) SetUint(x uint64) {
	v.mustValid("SetUint")
	if v.host() {
		v.rv.SetUint(x)
		return
	}
	v.mustBeSettable("SetUint")
	v.expectKind("SetUint", reflect.Uint, reflect.Uint8, reflect.Uint16,
		reflect.Uint32, reflect.Uint64, reflect.Uintptr)
	if x <= 0x7fffffffffffffff {
		v.set(truncInt(v.declTd(), int64(x)))
		return
	}
	v.set(&runtime.GoValue{V: x})
}

// SetFloat writes a float64.
func (v *RValue) SetFloat(x float64) {
	v.mustValid("SetFloat")
	if v.host() {
		v.rv.SetFloat(x)
		return
	}
	v.mustBeSettable("SetFloat")
	v.expectKind("SetFloat", reflect.Float32, reflect.Float64)
	v.set(x)
}

// SetComplex writes a complex value.
func (v *RValue) SetComplex(x complex128) {
	v.mustValid("SetComplex")
	if v.host() {
		v.rv.SetComplex(x)
		return
	}
	v.mustBeSettable("SetComplex")
	v.expectKind("SetComplex", reflect.Complex64, reflect.Complex128)
	v.set(&runtime.GoValue{V: x})
}

// SetString writes a string.
func (v *RValue) SetString(x string) {
	v.mustValid("SetString")
	if v.host() {
		v.rv.SetString(x)
		return
	}
	v.mustBeSettable("SetString")
	v.expectKind("SetString", reflect.String)
	v.set(x)
}

// SetBytes writes a []byte.
func (v *RValue) SetBytes(x []byte) {
	v.mustValid("SetBytes")
	if v.host() {
		v.rv.SetBytes(x)
		return
	}
	// Go's order: settable first, then the slice kind, then the
	// []uint8-element gate — whose panic carries no `reflect:` prefix.
	v.mustBeSettable("SetBytes")
	if v.Kind() != reflect.Slice {
		trap("call of reflect.Value.SetBytes on %s Value", v.kindStr())
	}
	if et := v.e.elemOf(v.td); et == nil || v.e.kindOfTd(et) != reflect.Uint8 {
		plain("reflect.Value.SetBytes of non-byte slice")
	}
	elems := make([]runtime.Value, len(x))
	for i, b := range x {
		elems[i] = int64(b)
	}
	v.set(&runtime.Slice{Elems: elems, Typ: &runtime.TypeDef{
		Kind: runtime.KindSlice, Elem: runtime.BasicTypedef("byte")}})
}

// SetLen is not part of reflect.Value — kept absent.

// ---- calls ----

// Call invokes a func value.
func (v *RValue) Call(in []*RValue) []*RValue {
	v.mustValid("Call")
	if v.host() {
		// Go checks the func kind before reading the signature — a
		// non-func receiver dies on 'call of reflect.Value.Call on
		// X Value', not on IsVariadic's 'non-func type' panic.
		if v.rv.Kind() != reflect.Func {
			v.rv.Call(nil)
		}
		args := make([]reflect.Value, len(in))
		mt := v.rv.Type()
		for i, a := range in {
			var pt reflect.Type
			if mt.IsVariadic() && i >= mt.NumIn()-1 {
				pt = mt.In(mt.NumIn() - 1).Elem()
			} else {
				pt = mt.In(i)
			}
			rv, err := toHost(a.ifaceVal(), pt)
			if err != nil {
				trap("reflect.Value.Call: %s", err)
			}
			args[i] = rv
		}
		out := v.rv.Call(args)
		var res []*RValue
		for _, o := range out {
			res = append(res, v.e.wrapHost(v.vc, o))
		}
		return res
	}
	v.expectKind("Call", reflect.Func)
	if v.vc == nil {
		trap("minireflect: reflect.Value.Call needs a caller context")
	}
	v.checkCallArgs(in, false)

	args := make([]runtime.Value, len(in))
	for i, a := range in {
		if a == nil {
			args[i] = runtime.NIL
			continue
		}
		args[i] = normVal(a.ifaceVal())
	}
	r, err := v.vc.Call(v.get(), args)
	if err != nil {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect.Value.Call: %s", err)})
	}
	return v.callOut(r)
}

// callOut marshals the callee's raw result into Go's result arity: the
// VM returns NIL for a void call, so the declared signature's result
// count decides how many RValues surface.
func (v *RValue) callOut(r runtime.Value) []*RValue {
	if ft := v.callSig(); ft != nil {
		n := 0
		if ft.Results != nil {
			for _, f := range ft.Results.List {
				if len(f.Names) > 0 {
					n += len(f.Names)
				} else {
					n++
				}
			}
		}
		if n == 0 {
			return nil
		}
	}
	if tup, ok := r.(*runtime.Tuple); ok {
		res := make([]*RValue, len(tup.Elems))
		for i, el := range tup.Elems {
			res[i] = v.e.wrap(v.vc, el, nil, typeOfValue(v.e, el))
		}
		return res
	}
	return []*RValue{v.e.wrap(v.vc, r, nil, typeOfValue(v.e, r))}
}

// checkCallArgs replays Go's arity and per-argument assignability
// gates for Call/CallSlice, so a bad call panics with `reflect: Call
// with too few input arguments` / `reflect: Call using X as type Y`
// before the callee runs.
func (v *RValue) checkCallArgs(in []*RValue, sliceMode bool) {
	ft := v.callSig()
	if ft == nil || ft.Params == nil {
		return
	}
	// count parameters, not field entries — `a, b int` is two.
	numIn := 0
	for _, f := range ft.Params.List {
		if n := len(f.Names); n > 0 {
			numIn += n
		} else {
			numIn++
		}
	}
	variadic := false
	if n := len(ft.Params.List); n > 0 {
		_, variadic = ft.Params.List[n-1].Type.(*ast.Ellipsis)
	}
	name := "Call"
	if sliceMode {
		name = "CallSlice"
	}
	switch {
	case len(in) < numIn && (!variadic || len(in) < numIn-1):
		plain("reflect: %s with too few input arguments", name)
	case len(in) > numIn && (!variadic || sliceMode):
		plain("reflect: %s with too many input arguments", name)
	}
	t := v.Type()
	if t == nil {
		return
	}
	for i, a := range in {
		var pt *RType
		switch {
		case sliceMode && i == numIn-1:
			pt = t.In(numIn - 1) // the whole []T tail param
		case variadic && !sliceMode && i >= numIn-1:
			if last := t.In(numIn - 1); last != nil {
				pt = last.Elem()
			}
		default:
			if i < numIn {
				pt = t.In(i)
			}
		}
		if a == nil || a.Type() == nil {
			plain("reflect: %s using zero Value argument", name)
		}
		if pt == nil {
			continue
		}
		if xt := a.Type(); xt != nil && !xt.AssignableTo(pt) {
			plain("reflect: %s using %s as type %s", name, xt.String(), pt.String())
		}
	}
}

// callSig finds the callee's declared signature — the function's own
// decl type, a closure's literal type, a bound method's decl type, or
// the typedef's FuncType spec when the value is opaque.
func (v *RValue) callSig() *ast.FuncType {
	var decl *ast.FuncDecl
	switch fn := v.get().(type) {
	case *runtime.Function:
		decl = fn.Decl
	case *runtime.Closure:
		if fn.Fn != nil {
			decl = fn.Fn.Decl
		}
	case *runtime.BoundMethod:
		if fn.Fn != nil {
			decl = fn.Fn.Decl
		}
	}
	if decl != nil && decl.Type != nil {
		return decl.Type
	}
	if t := v.Type(); t != nil {
		return funcSig(t)
	}
	return nil
}

// CallSlice invokes a variadic func value with a slice as the tail —
// Go assigns the slice to the variadic parameter, so the script side
// spreads its elements into the trailing args, mirroring `f(xs...)`.
func (v *RValue) CallSlice(in []*RValue) []*RValue {
	v.mustValid("CallSlice")
	if v.host() {
		// same order as Call: the kind check precedes the
		// signature reads.
		if v.rv.Kind() != reflect.Func {
			v.rv.CallSlice(nil)
		}
		mt := v.rv.Type()
		// Go rejects a non-variadic callee before counting or
		// marshalling any args — In(-1) on a niladic signature
		// would panic 'index out of range' instead.
		if !mt.IsVariadic() {
			plain("reflect: CallSlice of non-variadic function")
		}
		args := make([]reflect.Value, len(in))
		for i, a := range in {
			pt := mt.In(min(i, mt.NumIn()-1))
			rv, err := toHost(a.ifaceVal(), pt)
			if err != nil {
				trap("reflect.Value.CallSlice: %s", err)
			}
			args[i] = rv
		}
		out := v.rv.CallSlice(args)
		var res []*RValue
		for _, o := range out {
			res = append(res, v.e.wrapHost(v.vc, o))
		}
		return res
	}
	v.expectKind("CallSlice", reflect.Func)
	if v.vc == nil {
		trap("minireflect: reflect.Value.CallSlice needs a caller context")
	}
	if ft := v.callSig(); ft != nil {
		variadic := false
		if ft.Params != nil && len(ft.Params.List) > 0 {
			_, variadic = ft.Params.List[len(ft.Params.List)-1].Type.(*ast.Ellipsis)
		}
		if !variadic {
			trap("CallSlice of non-variadic function")
		}
	}
	if len(in) == 0 {
		trap("CallSlice with empty input slice")
	}
	v.checkCallArgs(in, true)

	last := in[len(in)-1]
	last.mustValid("CallSlice")
	var s *runtime.Slice
	switch x := last.get().(type) {
	case *runtime.Slice:
		s = x
	case *runtime.Named:
		s, _ = x.V.(*runtime.Slice)
	}
	if s == nil {
		trap("reflect.Value.CallSlice: last argument must be a slice")
	}
	spread := make([]*RValue, 0, len(in)-1+len(s.Elems))
	spread = append(spread, in[:len(in)-1]...)
	for _, el := range s.Elems {
		spread = append(spread, v.e.wrap(v.vc, el, nil, typeOfValue(v.e, el)))
	}
	return v.Call(spread)
}

// Method returns the value's i'th method (not supported: ordering
// script method sets needs the typedef machinery).
func (v *RValue) Method(i int) *RValue {
	v.mustValid("Method")
	if v.host() {
		return v.e.wrapHost(v.vc, v.rv.Method(i))
	}
	names := exportedMethodNames(v.e.methodSet(v.td))
	if i < 0 || i >= len(names) {
		panic(&runtime.Panic{Value: "reflect: Method index out of range"})
	}
	return v.MethodByName(names[i])
}

// NumMethod reports the exported bound method count.
func (v *RValue) NumMethod() int {
	v.mustValid("NumMethod")
	if v.host() {
		return v.rv.NumMethod()
	}
	return len(exportedMethodNames(v.e.methodSet(v.td)))
}

// MethodByName binds an exported method by name through the caller's
// member dispatch — unexported members stay invisible, like Go.
func (v *RValue) MethodByName(name string) *RValue {
	v.mustValid("MethodByName")
	if v.host() {
		m := v.rv.MethodByName(name)
		return v.e.wrapHost(v.vc, m)
	}
	if !ast.IsExported(name) {
		return &RValue{e: v.e, vc: v.vc}
	}
	if v.td != nil && v.td.Kind == runtime.KindInterface {
		// An interface-typed Value exposes only the interface's own
		// requirements — MethodByName filters through them like Go.
		inSet := false
		for _, n := range exportedMethodNames(v.e.methodSet(v.td)) {
			if n == name {
				inSet = true
				break
			}
		}
		if !inSet {
			return &RValue{e: v.e, vc: v.vc}
		}
		if v.IsNil() {
			// Method(i) forwards here — the panic spells "Method".
			panic(&runtime.Panic{Value: "reflect: Method on nil interface value"})
		}
	}
	if v.vc == nil {
		trap("minireflect: reflect.Value.MethodByName needs a caller context")
	}
	m, ok := v.vc.Member(v.get(), name)
	if !ok {
		return &RValue{e: v.e, vc: v.vc}
	}
	// Type() of a bound method value reports the signature with the
	// receiver consumed — func() int for T{}.M — which the member's own
	// typedef already spells.
	return v.e.wrap(v.vc, m, nil, typeOfValue(v.e, m))
}

// ---- conversions / misc ----

// Convert converts the value to type t for the subset the facade
// supports (numeric widening, string<->[]byte).
func (v *RValue) Convert(t *RType) *RValue {
	if !v.IsValid() {
		// Go's Convert dereferences the source type before any
		// validity gate, so a zero Value dies as a nil pointer
		// dereference rather than "call of ... on zero Value".
		panic(runtime.NilDerefPanic())
	}
	if t == nil {
		trap("reflect.Value.Convert to nil type")
	}
	if t.rt != nil {
		rv, err := toHost(v.ifaceVal(), t.rt)
		if err != nil {
			trap("reflect.Value.Convert: %s", err)
		}
		cv := rv.Convert(t.rt)
		return &RValue{e: v.e, vc: v.vc, rv: cv, ro: v.ro || !cv.CanInterface()}
	}
	if st := v.Type(); st != nil && !st.ConvertibleTo(t) {
		plain("reflect.Value.Convert: value of type %s cannot be converted to type %s",
			st.String(), t.String())
	}
	k := t.Kind()
	var out runtime.Value
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		// permissive read — a float or unsigned source converts too,
		// where the Int/Uint accessors would (correctly) kind-gate
		switch x := v.get().(type) {
		case float64:
			out = int64(x)
		case *runtime.Named:
			if f, ok := x.V.(float64); ok {
				out = int64(f)
			} else {
				out = v.convInt()
			}
		default:
			out = v.convInt()
		}
	case reflect.Float32, reflect.Float64:
		switch x := v.get().(type) {
		case int64:
			out = float64(x)
		case *runtime.Named:
			if i, ok := x.V.(int64); ok {
				out = float64(i)
			} else {
				out = v.Float()
			}
		default:
			out = v.Float()
		}
	case reflect.String:
		// Convert produces a string, it does not format: a []byte/[]rune
		// decodes to its contents and an integer to a one-rune string.
		switch x := v.get().(type) {
		case *runtime.Slice:
			var sb strings.Builder
			for _, el := range x.Elems {
				sb.WriteByte(byte(intOf(el)))
			}
			out = sb.String()
		case *runtime.Named:
			if s, ok := x.V.(*runtime.Slice); ok {
				var sb strings.Builder
				for _, el := range s.Elems {
					sb.WriteByte(byte(intOf(el)))
				}
				out = sb.String()
			} else if s, ok := x.V.(string); ok {
				out = s
			} else {
				out = string(rune(v.convInt()))
			}
		case string:
			out = x
		default:
			if k := v.Kind(); k >= reflect.Int && k <= reflect.Uintptr {
				out = string(rune(v.convInt()))
			} else {
				out = v.String()
			}
		}
	case reflect.Bool:
		out = v.Bool()
	case reflect.Slice:
		if v.Kind() == reflect.String {
			out = strToBytes(v.String())
		} else {
			out = v.get()
		}
	default:
		out = v.get()
	}
	if k == reflect.Interface {
		out = v.get()
	}
	ntd := t.td
	if ntd != nil && ntd.Name != "" && ntd.Kind == runtime.KindNamedBasic {
		out = &runtime.Named{Typ: ntd, V: out}
	}
	// the read-only flag is sticky through Convert like Go's flagRO —
	// an unexported-field value converts to an unexportable value.
	return &RValue{e: v.e, vc: v.vc, val: out, td: ntd, ro: v.ro}
}

// convInt reads an integer permissively for Convert — unlike Int it
// accepts either signedness, since a conversion rather than an
// accessor read is being performed.
func (v *RValue) convInt() int64 {
	if k := v.Kind(); k >= reflect.Uint && k <= reflect.Uintptr {
		return int64(v.Uint())
	}
	return v.Int()
}

// Comparable reports comparability.
func (v *RValue) Comparable() bool {
	return v.Type().Comparable()
}

// Equal reports Go's equality: mismatched types report false BEFORE
// the comparability check, same-typed uncomparable values panic, and
// two invalid Values compare equal. Same type delegates to value
// equality.
func (v *RValue) Equal(u *RValue) bool {
	vok := v != nil && v.IsValid()
	uok := u != nil && u.IsValid()
	if !vok || !uok {
		return vok == uok
	}
	if v.host() && u.host() {
		return v.rv.Equal(u.rv)
	}
	vt, ut := v.Type(), u.Type()
	if vt != ut {
		return false // reflect's Equal needs identical types — checked first
	}
	if vt != nil && !vt.Comparable() {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect.Value.Equal: values of type %s are not comparable", vt.String())})
	}
	return valueEqual(v.ifaceVal(), u.ifaceVal(), 0)
}

// Pointer / UnsafePointer / UnsafeAddr are the unsafe surface: the
// facade refuses them loudly.
func (v *RValue) Pointer() uintptr {
	trap("minireflect: reflect.Value.Pointer is not supported")
	return 0
}

// UnsafePointer is unsupported.
func (v *RValue) UnsafePointer() any {
	trap("minireflect: reflect.Value.UnsafePointer is not supported")
	return nil
}

// UnsafeAddr is unsupported.
func (v *RValue) UnsafeAddr() uintptr {
	trap("minireflect: reflect.Value.UnsafeAddr is not supported")
	return 0
}

// CanComplex / Overflow* follow.

// OverflowInt reports whether x fits the value's int kind.
func (v *RValue) OverflowInt(x int64) bool {
	v.mustValid("OverflowInt")
	if v.host() {
		return v.rv.OverflowInt(x)
	}
	switch v.Kind() {
	case reflect.Int:
		return x != int64(int(x))
	case reflect.Int8:
		return x < -128 || x > 127
	case reflect.Int16:
		return x < -32768 || x > 32767
	case reflect.Int32:
		return x < -2147483648 || x > 2147483647
	}
	plain("reflect: call of reflect.Value.OverflowInt on %s Value", v.kindStr())
	return false
}

// OverflowUint reports whether x fits the value's uint kind.
func (v *RValue) OverflowUint(x uint64) bool {
	v.mustValid("OverflowUint")
	if v.host() {
		return v.rv.OverflowUint(x)
	}
	switch v.Kind() {
	case reflect.Uint, reflect.Uint64, reflect.Uintptr:
		return x != uint64(uint(x))
	case reflect.Uint8:
		return x > 255
	case reflect.Uint16:
		return x > 65535
	case reflect.Uint32:
		return x > 4294967295
	}
	plain("reflect: call of reflect.Value.OverflowUint on %s Value", v.kindStr())
	return false
}

// OverflowFloat reports whether x fits a float32.
func (v *RValue) OverflowFloat(x float64) bool {
	v.mustValid("OverflowFloat")
	if v.host() {
		return v.rv.OverflowFloat(x)
	}
	switch v.Kind() {
	case reflect.Float32:
		return x > 3.4028234663852886e+38 || x < -3.4028234663852886e+38
	case reflect.Float64:
		return false
	}
	plain("reflect: call of reflect.Value.OverflowFloat on %s Value", v.kindStr())
	return false
}

// OverflowComplex reports whether x fits a complex64.
func (v *RValue) OverflowComplex(x complex128) bool {
	v.mustValid("OverflowComplex")
	if v.host() {
		return v.rv.OverflowComplex(x)
	}
	switch v.Kind() {
	case reflect.Complex64:
		const lim = 3.4028234663852886e+38
		return real(x) > lim || real(x) < -lim || imag(x) > lim || imag(x) < -lim
	case reflect.Complex128:
		return false
	}
	plain("reflect: call of reflect.Value.OverflowComplex on %s Value", v.kindStr())
	return false
}

// ---- channel ops ----

// Recv receives from a chan value.
func (v *RValue) Recv() (*RValue, bool) {
	v.mustValid("Recv")
	if v.host() {
		x, ok := v.rv.Recv()
		return v.e.wrapHost(v.vc, x), ok
	}
	if c, ok := v.get().(*runtime.Chan); ok {
		x, ok := <-c.C
		return v.e.wrap(v.vc, runtime.Copy(x), nil, v.e.elemOf(v.td)), ok
	}
	trap("call of reflect.Value.Recv on %s Value", v.kindStr())
	return nil, false
}

// Send sends on a chan value.
func (v *RValue) Send(x *RValue) {
	v.mustValid("Send")
	if v.host() {
		// Go checks the chan kind before touching the element — a
		// non-chan receiver dies on 'call of reflect.Value.Send on
		// X Value' even when the arg would not marshal. Let rv.Send
		// deliver that panic itself.
		if v.rv.Kind() != reflect.Chan {
			v.rv.Send(v.rv)
		}
		xr, err := toHost(x.ifaceVal(), v.rv.Type().Elem())
		if err != nil {
			trap("reflect.Value.Send: %s", err)
		}
		v.rv.Send(xr)
		return
	}
	if c, ok := v.get().(*runtime.Chan); ok {
		c.C <- x.ifaceVal()
		return
	}
	trap("call of reflect.Value.Send on %s Value", v.kindStr())
}

// TryRecv polls a chan value.
func (v *RValue) TryRecv() (*RValue, bool) {
	v.mustValid("TryRecv")
	if v.host() {
		x, ok := v.rv.TryRecv()
		return v.e.wrapHost(v.vc, x), ok
	}
	if c, ok := v.get().(*runtime.Chan); ok {
		select {
		case x, ok := <-c.C:
			return v.e.wrap(v.vc, runtime.Copy(x), nil, v.e.elemOf(v.td)), ok
		default:
			return &RValue{e: v.e, vc: v.vc}, false
		}
	}
	trap("call of reflect.Value.TryRecv on %s Value", v.kindStr())
	return nil, false
}

// TrySend attempts a non-blocking send.
func (v *RValue) TrySend(x *RValue) bool {
	v.mustValid("TrySend")
	if v.host() {
		// same order as Send: the receiver kind check precedes any
		// work on the argument.
		if v.rv.Kind() != reflect.Chan {
			v.rv.TrySend(v.rv)
		}
		xr, err := toHost(x.ifaceVal(), v.rv.Type().Elem())
		if err != nil {
			trap("reflect.Value.TrySend: %s", err)
		}
		return v.rv.TrySend(xr)
	}
	if c, ok := v.get().(*runtime.Chan); ok {
		select {
		case c.C <- x.ifaceVal():
			return true
		default:
			return false
		}
	}
	trap("call of reflect.Value.TrySend on %s Value", v.kindStr())
	return false
}

// Close closes a chan value.
func (v *RValue) Close() {
	v.mustValid("Close")
	if v.host() {
		v.rv.Close()
		return
	}
	if c, ok := v.get().(*runtime.Chan); ok {
		close(c.C)
		return
	}
	trap("call of reflect.Value.Close on %s Value", v.kindStr())
}

// ---- helpers ----

func isExported(name string) bool {
	if name == "" {
		return false
	}
	return unicode.IsUpper(rune(name[0]))
}

// arrayTypeOf returns td's underlying ArrayType when td spells a
// fixed-size array — its AST carries a length (slices have none),
// mirroring runtime's arrayTypedef.
func arrayTypeOf(td *runtime.TypeDef) *ast.ArrayType {
	if td == nil {
		return nil
	}
	x := td.Anon
	if x == nil && td.Spec != nil {
		x = td.Spec.Type
	}
	at, _ := x.(*ast.ArrayType)
	if at != nil && at.Len != nil {
		return at
	}
	return nil
}

// unwrapRef strips Named wrappers down to the ref-view underneath so
// pointer mechanics can reach the cell.
func unwrapRef(v runtime.Value) runtime.Value {
	for {
		if n, ok := v.(*runtime.Named); ok {
			v = n.V
			continue
		}
		return v
	}
}

// normVal normalizes an ifaceVal payload back into a script value:
// runtime values pass through; bare host objects get their GoValue box.
func normVal(v any) runtime.Value {
	switch v.(type) {
	case nil:
		return runtime.NIL
	case runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil,
		*runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef,
		*runtime.Slice, *runtime.Map, *runtime.Struct, *runtime.Chan,
		*runtime.Function, *runtime.Closure, *runtime.BoundMethod,
		*runtime.BuiltinFunc, *runtime.Named, *runtime.UConst,
		*runtime.Tuple, *runtime.GoValue, *runtime.TypeDef, *runtime.Package,
		int64, float64, string, bool:
		return v
	}
	return &runtime.GoValue{V: v}
}

// strToBytes builds a script []byte from a string.
func strToBytes(s string) *runtime.Slice {
	elems := make([]runtime.Value, len(s))
	for i := 0; i < len(s); i++ {
		elems[i] = int64(s[i])
	}
	return &runtime.Slice{Elems: elems, Typ: &runtime.TypeDef{
		Kind: runtime.KindSlice, Elem: &runtime.TypeDef{Name: "byte"}}}
}

// toHost marshals a script-side value into a host reflect.Value for
// the target type — the narrow bridge used when a host-domain value
// absorbs a script one.
func toHost(v any, t reflect.Type) (reflect.Value, error) {
	switch x := v.(type) {
	case nil, runtime.Nil:
		return reflect.Zero(t), nil
	case *runtime.GoValue:
		rv := reflect.ValueOf(x.V)
		if rv.IsValid() && rv.Type().AssignableTo(t) {
			return rv, nil
		}
		if rv.IsValid() && rv.Type().ConvertibleTo(t) {
			return rv.Convert(t), nil
		}
		return reflect.Value{}, fmt.Errorf("cannot use %T as %s", x.V, t)
	case int64:
		switch {
		case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
			rv := reflect.New(t).Elem()
			rv.SetInt(x)
			return rv, nil
		case t.Kind() >= reflect.Uint && t.Kind() <= reflect.Uintptr:
			rv := reflect.New(t).Elem()
			rv.SetUint(uint64(x))
			return rv, nil
		case t.Kind() >= reflect.Float32 && t.Kind() <= reflect.Float64:
			rv := reflect.New(t).Elem()
			rv.SetFloat(float64(x))
			return rv, nil
		case t.Kind() == reflect.Interface:
			return reflect.ValueOf(x), nil
		}
	case float64:
		if t.Kind() >= reflect.Float32 && t.Kind() <= reflect.Float64 {
			rv := reflect.New(t).Elem()
			rv.SetFloat(x)
			return rv, nil
		}
	case string:
		if t.Kind() == reflect.String {
			rv := reflect.New(t).Elem()
			rv.SetString(x)
			return rv, nil
		}
	case bool:
		if t.Kind() == reflect.Bool {
			rv := reflect.New(t).Elem()
			rv.SetBool(x)
			return rv, nil
		}
	case *runtime.Named:
		return toHost(x.V, t)
	}
	if rv := reflect.ValueOf(v); rv.IsValid() && rv.Type().AssignableTo(t) {
		return rv, nil
	}
	if t.Kind() == reflect.Interface && t.NumMethod() == 0 {
		rv := reflect.New(t).Elem()
		rv.Set(reflect.ValueOf(v))
		return rv, nil
	}
	return reflect.Value{}, fmt.Errorf("cannot marshal %T to %s", v, t)
}

// valueEqual is a small structural equality for Equal().
func valueEqual(a, b any, depth int) bool {
	if depth > 32 {
		return true // cycle break
	}
	if a == nil && b == nil {
		return true
	}
	switch x := a.(type) {
	case int64:
		y, ok := b.(int64)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case *runtime.Named:
		if y, ok := b.(*runtime.Named); ok {
			return valueEqual(x.V, y.V, depth+1)
		}
		return valueEqual(x.V, b, depth+1)
	case *runtime.Struct:
		if y, ok := b.(*runtime.Struct); ok {
			if len(x.Fields) != len(y.Fields) {
				return false
			}
			for i := range x.Fields {
				if !valueEqual(x.Fields[i], y.Fields[i], depth+1) {
					return false
				}
			}
			return true
		}
		return false
	case *runtime.Slice:
		if y, ok := b.(*runtime.Slice); ok {
			if len(x.Elems) != len(y.Elems) {
				return false
			}
			for i := range x.Elems {
				if !valueEqual(x.Elems[i], y.Elems[i], depth+1) {
					return false
				}
			}
			return true
		}
		return false
	case *runtime.Map:
		if y, ok := b.(*runtime.Map); ok {
			if x.Len() != y.Len() {
				return false
			}
			for _, k := range x.Order {
				xv, _ := x.Get(k)
				yv, ok := y.Get(k)
				if !ok || !valueEqual(xv, yv, depth+1) {
					return false
				}
			}
			return true
		}
		return false
	case *runtime.GoValue:
		if y, ok := b.(*runtime.GoValue); ok {
			return reflect.DeepEqual(x.V, y.V)
		}
		return false
	}
	return a == b
}
