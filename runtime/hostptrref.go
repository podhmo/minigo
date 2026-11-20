package runtime

import (
	"reflect"
	"sync"
)

// HostPtrRef is the location `*p` names when p is a host pointer box — a
// [GoValue] holding a non-nil reflect pointer. Reads box the pointee,
// writes store an assignable host value through the pointer. It is the
// host counterpart of the script storage refs (Cell, FieldRef,
// IndexRef); a pointee whose write needs a script→host conversion
// reports unset, like an unresolved location.
type HostPtrRef struct {
	V *GoValue // boxes a non-nil reflect.Ptr
}

// hostPtrLoc resolves v to the [HostPtrRef] a boxed host pointer names:
// the pointer itself when v is one, or the value one dereference in
// when v is the storage ref holding it (the DerefRef operand is the
// ref to the pointer's storage, not the pointer).
func hostPtrLoc(v Value) (*HostPtrRef, bool) {
	switch r := v.(type) {
	case *Named:
		return hostPtrLoc(r.V)
	case *GoValue:
		rv := reflect.ValueOf(r.V)
		if rv.IsValid() && rv.Kind() == reflect.Ptr && !rv.IsNil() {
			return &HostPtrRef{V: r}, true
		}
	}
	return nil, false
}

// HostPtrLoc resolves v to the location a boxed host pointer names —
// nil-able: a nil host pointer dereferences to an unresolved location
// like a nil script pointer.
func HostPtrLoc(v Value) (*HostPtrRef, bool) { return hostPtrLoc(v) }

// DerefValue resolves a *p read one level: a host pointer box names
// its pointee's storage, any other value goes through [Deref]. Only
// the pointee-read paths use it — [Deref] itself stays ref-only so
// peel loops keep ending on a host leaf.
func DerefValue(v Value) (Value, bool) {
	if loc, ok := hostPtrLoc(v); ok {
		return loc.Get()
	}
	return Deref(v)
}

// elem returns the reflect.Value the pointer's pointee addressable
// storage exposes.
func (r *HostPtrRef) elem() (reflect.Value, bool) {
	rv := reflect.ValueOf(r.V.V)
	if !rv.IsValid() || rv.Kind() != reflect.Ptr || rv.IsNil() {
		return reflect.Value{}, false
	}
	return rv.Elem(), true
}

// hostStructTyp mints the value-side typedef of a host struct's
// pointee — KindStruct with the reflect identity carried so the intern
// key lands on the same descriptor the bound type's typedef keys, and
// a HostNew so assignment copies like `var b bytes.Buffer` does. One
// descriptor per reflect type, interned here since runtime cannot
// reach the engine's table.
var hostStructTyps sync.Map // reflect.Type -> *TypeDef

func hostStructTyp(rt reflect.Type) *TypeDef {
	if td, ok := hostStructTyps.Load(rt); ok {
		return td.(*TypeDef)
	}
	td := &TypeDef{
		Name:      rt.String(),
		Kind:      KindStruct,
		HostIdent: rt,
		HostNew:   func() any { return reflect.New(rt).Interface() },
	}
	got, _ := hostStructTyps.LoadOrStore(rt, td)
	return got.(*TypeDef)
}

// Get boxes the pointee for a script read: the `*p` value. A struct
// pointee comes back as Named{td(T), GoValue{*T over the pointee's
// storage}} — the shape a `var t T` carries — so `r := *p` copies the
// struct, `(*p).f` member writes and pointer-receiver methods reach
// the host storage, and `%T` spells T. A non-struct pointee returns
// its value outright.
func (r *HostPtrRef) Get() (Value, bool) {
	el, ok := r.elem()
	if !ok {
		return nil, false
	}
	if el.Kind() == reflect.Struct {
		return &Named{Typ: hostStructTyp(el.Type()), V: &GoValue{V: el.Addr().Interface()}}, true
	}
	return &GoValue{V: el.Interface()}, true
}

// Set stores v through the pointer when it is already a host value of
// an assignable type; a script value reports unset — converting it is
// the marshaller's job, which a bare location cannot reach.
func (r *HostPtrRef) Set(v Value) bool {
	el, ok := r.elem()
	if !ok || !el.CanSet() {
		return false
	}
	uv := v
	for {
		if n, isN := uv.(*Named); isN {
			uv = n.V
			continue
		}
		break
	}
	gv, ok := uv.(*GoValue)
	if !ok {
		return false
	}
	sv := reflect.ValueOf(gv.V)
	if !sv.IsValid() {
		el.Set(reflect.Zero(el.Type()))
		return true
	}
	if !sv.Type().AssignableTo(el.Type()) {
		// a host struct value lives in a *T box (the `var t T` shape
		// Get mints), so `*p = *q` arrives as *T for a T slot: the
		// assignment stores the pointee's struct.
		if sv.Kind() != reflect.Ptr || sv.IsNil() || !sv.Elem().Type().AssignableTo(el.Type()) {
			return false
		}
		sv = sv.Elem()
	}
	el.Set(sv)
	return true
}
