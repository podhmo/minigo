package minireflect

import (
	"fmt"
	"go/constant"
	"reflect"
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

// mustValid traps on a zero/invalid Value like reflect does.
func (v *RValue) mustValid() {
	if !v.IsValid() {
		trap("call of method on zero Value")
	}
}

// kindStr renders a kind for messages.
func (v *RValue) kindStr() string {
	return v.Kind().String()
}

// wrap builds a script-domain rvalue view.
func (e *Env) wrap(vc runtime.VMCaller, val, ref runtime.Value, td *runtime.TypeDef) *RValue {
	return &RValue{e: e, vc: vc, val: val, ref: ref, td: td}
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
		return &runtime.TypeDef{Kind: runtime.KindSlice, Elem: et}
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
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: et}
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
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: et}
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
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: et}
	case *runtime.DerefRef:
		var et *runtime.TypeDef
		if dv, ok := runtime.Deref(v); ok {
			et = typeOfValue(e, dv)
		}
		return &runtime.TypeDef{Kind: runtime.KindPointer, Elem: et}
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return &runtime.TypeDef{Kind: runtime.KindFunc}
	case *runtime.GoValue:
		if x.V == nil {
			return nil
		}
		if rv, ok := x.V.(*RValue); ok {
			return rv.staticTd()
		}
		return &runtime.TypeDef{Name: reflect.TypeOf(x.V).String()}
	case int64:
		return &runtime.TypeDef{Name: "int"}
	case float64:
		return &runtime.TypeDef{Name: "float64"}
	case string:
		return &runtime.TypeDef{Name: "string"}
	case bool:
		return &runtime.TypeDef{Name: "bool"}
	case *runtime.UConst:
		switch x.V.Kind() {
		case constant.Bool:
			return &runtime.TypeDef{Name: "bool"}
		case constant.String:
			return &runtime.TypeDef{Name: "string"}
		case constant.Float:
			return &runtime.TypeDef{Name: "float64"}
		case constant.Complex:
			return &runtime.TypeDef{Name: "complex128"}
		default:
			return &runtime.TypeDef{Name: "int"}
		}
	}
	return nil
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
	v.mustValid()
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
	v.mustValid()
	if v.ro {
		trap("reflect.Value.Interface: cannot return value obtained from unexported field or method")
	}
	return v.ifaceVal()
}

// CanInterface reports whether Interface is legal.
func (v *RValue) CanInterface() bool {
	return v.IsValid() && !v.ro
}

// Elem dereferences a pointer or interface value.
func (v *RValue) Elem() *RValue {
	v.mustValid()
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
		// the pointee is addressable: writes go back through the ptr
		return v.e.wrap(v.vc, dv, ptr, v.e.elemOf(v.td))
	case reflect.Interface:
		d := v.get()
		if d == nil || d == runtime.NIL {
			return &RValue{e: v.e, vc: v.vc}
		}
		if tn, isNil := d.(*runtime.TypedNil); isNil {
			// an interface holding a typed nil: Elem exposes the typed
			// nil's value like Go's v.Elem() on a non-nil interface
			return v.e.wrap(v.vc, tn, nil, tn.Typ)
		}
		return v.e.wrap(v.vc, d, nil, typeOfValue(v.e, d))
	}
	trap("call of reflect.Value.Elem on %s Value", v.kindStr())
	return nil
}

// Addr takes the address of an addressable value.
func (v *RValue) Addr() *RValue {
	v.mustValid()
	if v.host() {
		if !v.rv.CanAddr() {
			trap("call of reflect.Value.Addr on unaddressable value")
		}
		return v.e.wrapHost(v.vc, v.rv.Addr())
	}
	if v.ref == nil {
		trap("call of reflect.Value.Addr on unaddressable value")
	}
	ptd := &runtime.TypeDef{Kind: runtime.KindPointer, Elem: v.td}
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

// IsNil reports nil-ness for nilable kinds.
func (v *RValue) IsNil() bool {
	v.mustValid()
	if v.host() {
		switch v.rv.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
			reflect.Ptr, reflect.Slice, reflect.UnsafePointer:
			return v.rv.IsNil()
		}
		trap("call of reflect.Value.IsNil on %s Value", v.kindStr())
	}
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

// IsZero reports whether the value is its type's zero.
func (v *RValue) IsZero() bool {
	if !v.IsValid() {
		return true
	}
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
		return t == nil || (len(t.Elems) == 0)
	case *runtime.Map:
		return t == nil || t.Len() == 0
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
	v.mustValid()
	if v.host() {
		rv := v.rv
		if rv.Kind() == reflect.Ptr {
			rv = rv.Elem()
		}
		if rv.Kind() != reflect.Struct {
			trap("call of reflect.Value.Field on %s Value", v.kindStr())
		}
		f := rv.Field(i)
		return &RValue{e: v.e, vc: v.vc, rv: f, ro: !f.CanInterface()}
	}
	s := structOf(v.get())
	if s == nil {
		trap("call of reflect.Value.Field on %s Value", v.kindStr())
	}
	if i < 0 || i >= len(s.Fields) {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect: Field index %d out of range", i)})
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
	return &RValue{
		e: v.e, vc: v.vc,
		val: s.Fields[i],
		ref: &runtime.FieldRef{Base: s, Name: name},
		td:  ftd,
		ro:  ro,
	}
}

// NumField reports a struct value's field count.
func (v *RValue) NumField() int {
	v.mustValid()
	if v.host() {
		rv := v.rv
		for rv.Kind() == reflect.Ptr {
			rv = rv.Elem()
		}
		return rv.NumField()
	}
	if s := structOf(v.get()); s != nil {
		return len(s.Fields)
	}
	trap("call of reflect.Value.NumField on %s Value", v.kindStr())
	return 0
}

// FieldByIndex resolves a nested field path.
func (v *RValue) FieldByIndex(idx []int) *RValue {
	cur := v
	for _, i := range idx {
		cur = cur.Field(i)
	}
	return cur
}

// FieldByName looks up a struct field by name (no promotion).
func (v *RValue) FieldByName(name string) *RValue {
	v.mustValid()
	if v.host() {
		rv := v.rv
		for rv.Kind() == reflect.Ptr {
			rv = rv.Elem()
		}
		f := rv.FieldByName(name)
		return &RValue{e: v.e, vc: v.vc, rv: f, ro: !f.CanInterface()}
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
	return &RValue{e: v.e, vc: v.vc}
}

// Len reports len() of a slice/array/string/map/chan value.
func (v *RValue) Len() int {
	v.mustValid()
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
		return v.e.wrap(v.vc, x.V, v.ref, v.td).Len()
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
	v.mustValid()
	if v.ref == nil {
		trap("reflect.Value.SetLen using unaddressable value")
	}
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
		trap("call of reflect.Value.SetLen on %s Value", v.kindStr())
	}
	if n < 0 || n > cap(s.Elems) {
		trap("reflect.Value.SetLen: length %d exceeds capacity %d", n, cap(s.Elems))
	}
	v.set(&runtime.Slice{Elems: s.Elems[:n], Typ: s.Typ})
}

// Cap reports cap() of a slice/array/chan value.
func (v *RValue) Cap() int {
	v.mustValid()
	if v.host() {
		return v.rv.Cap()
	}
	switch x := v.get().(type) {
	case *runtime.Slice:
		return cap(x.Elems)
	case *runtime.Chan:
		return cap(x.C)
	case *runtime.Named:
		return v.e.wrap(v.vc, x.V, v.ref, v.td).Cap()
	}
	trap("call of reflect.Value.Cap on %s Value", v.kindStr())
	return 0
}

// Index reports a slice/array/string's i'th element.
func (v *RValue) Index(i int) *RValue {
	v.mustValid()
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
			panic(&runtime.Panic{Value: fmt.Sprintf("reflect: slice index %d out of range", i)})
		}
		return &RValue{e: v.e, vc: v.vc, val: x.Elems[i],
			ref: &runtime.IndexRef{Base: x, Key: int64(i)}, td: etd, ro: v.ro}
	case string:
		if i < 0 || i >= len(x) {
			panic(&runtime.Panic{Value: fmt.Sprintf("reflect: string index %d out of range", i)})
		}
		return &RValue{e: v.e, vc: v.vc, val: int64(x[i]), td: &runtime.TypeDef{Name: "uint8"}}
	case *runtime.Named:
		return v.e.wrap(v.vc, x.V, v.ref, v.td).Index(i)
	}
	trap("call of reflect.Value.Index on %s Value", v.kindStr())
	return nil
}

// Slice produces a subslice view sharing the backing store.
func (v *RValue) Slice(i, j int) *RValue {
	v.mustValid()
	if v.host() {
		return v.e.wrapHost(v.vc, v.rv.Slice(i, j))
	}
	if s, ok := v.get().(*runtime.Slice); ok {
		return v.e.wrap(v.vc, &runtime.Slice{Elems: s.Elems[i:j], Typ: s.Typ}, nil, v.td)
	}
	trap("call of reflect.Value.Slice on %s Value", v.kindStr())
	return nil
}

// MapIndex looks up a map value; missing keys give an invalid Value.
func (v *RValue) MapIndex(k *RValue) *RValue {
	v.mustValid()
	if v.host() {
		kr, err := toHost(k.ifaceVal(), v.rv.Type().Key())
		if err != nil {
			trap("reflect.Value.MapIndex: %s", err)
		}
		return &RValue{e: v.e, vc: v.vc, rv: v.rv.MapIndex(kr)}
	}
	m, ok := v.get().(*runtime.Map)
	if !ok {
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
	v.mustValid()
	if v.host() {
		var out []*RValue
		for _, k := range v.rv.MapKeys() {
			out = append(out, v.e.wrapHost(v.vc, k))
		}
		return out
	}
	m, ok := v.get().(*runtime.Map)
	if !ok {
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
	v.mustValid()
	if v.host() {
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
	if !ok {
		trap("call of reflect.Value.SetMapIndex on %s Value", v.kindStr())
	}
	if !x.IsValid() {
		m.Delete(normVal(k.ifaceVal()))
		return
	}
	m.Insert(normVal(k.ifaceVal()), normVal(x.ifaceVal()))
}

// MapRange starts a map iteration.
func (v *RValue) MapRange() *MapIter {
	v.mustValid()
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

// Next advances the iterator.
func (it *MapIter) Next() bool {
	if it.hit != nil {
		return it.hit.Next()
	}
	it.i++
	return it.i <= len(it.keys)
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

// SetIterKey assigns the current iteration key.
func (v *RValue) SetIterKey(it *MapIter) {
	v.Set(it.Key())
}

// SetIterValue assigns the current iteration value.
func (v *RValue) SetIterValue(it *MapIter) {
	v.Set(it.Value())
}

// ---- scalar reads ----

// Int reads an integer value.
func (v *RValue) Int() int64 {
	v.mustValid()
	if v.host() {
		return v.rv.Int()
	}
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
	v.mustValid()
	if v.host() {
		return v.rv.Uint()
	}
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
	v.mustValid()
	if v.host() {
		return v.rv.Float()
	}
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
	v.mustValid()
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

// Bytes reads a []byte value.
func (v *RValue) Bytes() []byte {
	v.mustValid()
	if v.host() {
		return v.rv.Bytes()
	}
	switch x := v.get().(type) {
	case string:
		return []byte(x)
	case *runtime.Slice:
		out := make([]byte, len(x.Elems))
		for i, el := range x.Elems {
			out[i] = byte(intOf(el))
		}
		return out
	case *runtime.Named:
		return v.e.wrap(v.vc, x.V, v.ref, v.td).Bytes()
	}
	trap("call of reflect.Value.Bytes on %s Value", v.kindStr())
	return nil
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
	v.mustValid()
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

// Set assigns another value's content.
func (v *RValue) Set(x *RValue) {
	v.mustValid()
	if x == nil || !x.IsValid() {
		trap("reflect.Value.Set: value of type %s is not assignable to type %s",
			"<invalid>", v.Type().String())
	}
	if v.host() {
		rv, err := toHost(x.ifaceVal(), v.rv.Type())
		if err != nil {
			trap("reflect.Value.Set: %s", err)
		}
		v.rv.Set(rv)
		return
	}
	val := x.get()
	if x.host() {
		val = &runtime.GoValue{V: x.rv.Interface()}
	}
	v.set(val)
}

// SetBool writes a bool.
func (v *RValue) SetBool(b bool) {
	v.mustValid()
	if v.host() {
		v.rv.SetBool(b)
		return
	}
	v.set(b)
}

// SetInt writes an int64.
func (v *RValue) SetInt(x int64) {
	v.mustValid()
	if v.host() {
		v.rv.SetInt(x)
		return
	}
	v.set(x)
}

// SetUint writes a uint64 (kept as int64 in the script domain).
func (v *RValue) SetUint(x uint64) {
	v.mustValid()
	if v.host() {
		v.rv.SetUint(x)
		return
	}
	if x <= 0x7fffffffffffffff {
		v.set(int64(x))
		return
	}
	v.set(&runtime.GoValue{V: x})
}

// SetFloat writes a float64.
func (v *RValue) SetFloat(x float64) {
	v.mustValid()
	if v.host() {
		v.rv.SetFloat(x)
		return
	}
	v.set(x)
}

// SetComplex writes a complex value.
func (v *RValue) SetComplex(x complex128) {
	v.mustValid()
	if v.host() {
		v.rv.SetComplex(x)
		return
	}
	v.set(&runtime.GoValue{V: x})
}

// SetString writes a string.
func (v *RValue) SetString(x string) {
	v.mustValid()
	if v.host() {
		v.rv.SetString(x)
		return
	}
	v.set(x)
}

// SetBytes writes a []byte.
func (v *RValue) SetBytes(x []byte) {
	v.mustValid()
	if v.host() {
		v.rv.SetBytes(x)
		return
	}
	elems := make([]runtime.Value, len(x))
	for i, b := range x {
		elems[i] = int64(b)
	}
	v.set(&runtime.Slice{Elems: elems, Typ: &runtime.TypeDef{
		Kind: runtime.KindSlice, Elem: &runtime.TypeDef{Name: "byte"}}})
}

// SetLen is not part of reflect.Value — kept absent.

// ---- calls ----

// Call invokes a func value.
func (v *RValue) Call(in []*RValue) []*RValue {
	v.mustValid()
	if v.host() {
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
	if v.vc == nil {
		trap("minireflect: reflect.Value.Call needs a caller context")
	}
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
	if tup, ok := r.(*runtime.Tuple); ok {
		res := make([]*RValue, len(tup.Elems))
		for i, el := range tup.Elems {
			res[i] = v.e.wrap(v.vc, el, nil, typeOfValue(v.e, el))
		}
		return res
	}
	return []*RValue{v.e.wrap(v.vc, r, nil, typeOfValue(v.e, r))}
}

// CallSlice invokes a variadic func value with a slice as the tail.
func (v *RValue) CallSlice(in []*RValue) []*RValue {
	return v.Call(in)
}

// Method returns the value's i'th method (not supported: ordering
// script method sets needs the typedef machinery).
func (v *RValue) Method(i int) *RValue {
	v.mustValid()
	if v.host() {
		return v.e.wrapHost(v.vc, v.rv.Method(i))
	}
	names := sortedKeys(v.e.typeMethods(v.td))
	if i < 0 || i >= len(names) {
		panic(&runtime.Panic{Value: fmt.Sprintf("reflect: Method index %d out of range", i)})
	}
	return v.MethodByName(names[i])
}

// NumMethod reports the bound method count.
func (v *RValue) NumMethod() int {
	v.mustValid()
	if v.host() {
		return v.rv.NumMethod()
	}
	return len(v.e.typeMethods(v.td))
}

// MethodByName binds a method by name through the caller's member
// dispatch.
func (v *RValue) MethodByName(name string) *RValue {
	v.mustValid()
	if v.host() {
		m := v.rv.MethodByName(name)
		return v.e.wrapHost(v.vc, m)
	}
	if v.vc == nil {
		trap("minireflect: reflect.Value.MethodByName needs a caller context")
	}
	m, ok := v.vc.Member(v.get(), name)
	if !ok {
		return &RValue{e: v.e, vc: v.vc}
	}
	return v.e.wrap(v.vc, m, nil, &runtime.TypeDef{Kind: runtime.KindFunc})
}

// ---- conversions / misc ----

// Convert converts the value to type t for the subset the facade
// supports (numeric widening, string<->[]byte).
func (v *RValue) Convert(t *RType) *RValue {
	v.mustValid()
	if t == nil {
		trap("reflect.Value.Convert to nil type")
	}
	if t.rt != nil {
		rv, err := toHost(v.ifaceVal(), t.rt)
		if err != nil {
			trap("reflect.Value.Convert: %s", err)
		}
		return v.e.wrapHost(v.vc, rv.Convert(t.rt))
	}
	k := t.Kind()
	var out runtime.Value
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		out = v.Int()
	case reflect.Float32, reflect.Float64:
		out = v.Float()
	case reflect.String:
		out = v.String()
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
	return v.e.wrap(v.vc, out, nil, ntd)
}

// Comparable reports comparability.
func (v *RValue) Comparable() bool {
	return v.Type().Comparable()
}

// Equal reports deep-ish equality against u.
func (v *RValue) Equal(u *RValue) bool {
	if u == nil || !u.IsValid() {
		return !v.IsValid()
	}
	if v.host() && u.host() {
		return v.rv.Equal(u.rv)
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
	if v.host() {
		return v.rv.OverflowInt(x)
	}
	switch v.Kind() {
	case reflect.Int8:
		return x < -128 || x > 127
	case reflect.Int16:
		return x < -32768 || x > 32767
	case reflect.Int32:
		return x < -2147483648 || x > 2147483647
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32,
		reflect.Uint64, reflect.Uintptr:
		return x < 0
	}
	return false
}

// OverflowUint reports whether x fits the value's uint kind.
func (v *RValue) OverflowUint(x uint64) bool {
	if v.host() {
		return v.rv.OverflowUint(x)
	}
	switch v.Kind() {
	case reflect.Uint8:
		return x > 255
	case reflect.Uint16:
		return x > 65535
	case reflect.Uint32:
		return x > 4294967295
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return x > 0x7fffffffffffffff
	}
	return false
}

// OverflowFloat reports whether x fits a float32.
func (v *RValue) OverflowFloat(x float64) bool {
	if v.host() {
		return v.rv.OverflowFloat(x)
	}
	if v.Kind() == reflect.Float32 {
		return x > 3.4028234663852886e+38 || x < -3.4028234663852886e+38
	}
	return false
}

// OverflowComplex reports whether x fits a complex64.
func (v *RValue) OverflowComplex(x complex128) bool {
	if v.host() {
		return v.rv.OverflowComplex(x)
	}
	if v.Kind() == reflect.Complex64 {
		const lim = 3.4028234663852886e+38
		return real(x) > lim || real(x) < -lim || imag(x) > lim || imag(x) < -lim
	}
	return false
}

// ---- channel ops ----

// Recv receives from a chan value.
func (v *RValue) Recv() (*RValue, bool) {
	v.mustValid()
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
	v.mustValid()
	if v.host() {
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
	v.mustValid()
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
	v.mustValid()
	if v.host() {
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
	v.mustValid()
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
