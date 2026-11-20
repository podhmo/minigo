package runtime

import "reflect"

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

// Get boxes the pointee for a script read: the `*p` value. The box
// holds *T over the pointee's own storage — the shape a `var t T`
// carries — so member writes through `(*p).f` and pointer-receiver
// methods reach the host storage.
func (r *HostPtrRef) Get() (Value, bool) {
	el, ok := r.elem()
	if !ok {
		return nil, false
	}
	return &GoValue{V: el.Addr().Interface()}, true
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
		return false
	}
	el.Set(sv)
	return true
}
