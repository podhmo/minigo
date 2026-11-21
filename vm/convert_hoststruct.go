package vm

import (
	"fmt"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// convertStruct implements `S(x)` on struct typedefs: legal when the
// source is a struct whose field list matches the target's (the
// approximation of Go's identical-underlying rule — field types and tags
// are not modeled). The result is a copy carrying the target's typedef.
// srcTag is the declared tag convert peeled off x, when it had one.
func (v *VM) convertStruct(td *runtime.TypeDef, x runtime.Value, srcTag *runtime.TypeDef) (runtime.Value, error) {
	s, ok := x.(*runtime.Struct)
	if !ok {
		if dv, isRef := runtime.Deref(x); isRef {
			s, ok = dv.(*runtime.Struct)
		}
	}
	if !ok {
		// a host struct box converts to a declared type over the same
		// host struct — `type MyBuf bytes.Buffer; MyBuf(b)` — or to
		// the bound type itself; gc copies the struct, so the tagged
		// result wraps a snapshot, not the shared box. The source must
		// BE the value: `var b bytes.Buffer` reads with the struct's
		// declared tag, while a real host pointer (NewBufferString's
		// *bytes.Buffer) has no value tag — `MyBuf(p)` stays a convert
		// error like gc.
		if u := v.peelNamed(td); u != nil && u.HostNew != nil {
			if gv, isGV := v.hostStructBox(x, u, srcTag); isGV {
				want := hostbridge.TypeOf(u.HostNew())
				wantPtr := want != nil && want.Kind() == hostbridge.KindPtr
				if nv, ok := runtime.HostStructSnapshot(gv, wantPtr); ok {
					return &runtime.Named{Typ: td, V: nv}, nil
				}
			}
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if s.Def != nil && s.Def != td && !runtime.StructFieldsEq(s.Def, td) {
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(s.Def), tdName(td))
	}
	return &runtime.Struct{Def: td, Fields: append([]runtime.Value{}, s.Fields...)}, nil
}

// hostStructBox reports whether x boxes the host struct value u names,
// returning the box. A `var b bytes.Buffer` slot carries the struct's
// declared tag, so the tag convert peeled off (srcTag) must name u's
// chain. An untagged box is only the value when it holds T itself (a
// host call returning the struct); one holding *T is a real host
// pointer — `MyBuf(p)` must fail like gc.
func (v *VM) hostStructBox(x runtime.Value, u, srcTag *runtime.TypeDef) (*runtime.GoValue, bool) {
	gv, ok := runtime.Unwrap(x).(*runtime.GoValue)
	if !ok {
		return nil, false
	}
	want := hostbridge.TypeOf(u.HostNew())
	if want == nil {
		return nil, false
	}
	// a bound type's HostNew spells its struct either as T (time.Time)
	// or *T (bytes.Buffer); the value a T box may hold is T either way.
	wantT := want
	if want.Kind() == hostbridge.KindPtr {
		wantT = want.Elem()
	}
	if wantT.Kind() != hostbridge.KindStruct {
		return nil, false
	}
	rt := hostbridge.TypeOf(gv.V)
	// a T-typed source's box holds T or *T — `var b T` storage and a
	// snapshot alike are the struct's storage either way.
	rtT := rt
	if rt != nil && rt.Kind() == hostbridge.KindPtr {
		rtT = rt.Elem()
	}
	if srcTag != nil {
		// the declared tag names the source's type: a `var b T` slot
		// tags T (peels to u); a *T tag peels to a pointer, not u.
		tag := v.peelNamed(srcTag)
		if tag == nil {
			tag = srcTag
		}
		if !sameTypeDef(tag, u) || rtT != wantT {
			return nil, false
		}
		return gv, true
	}
	if rt == wantT {
		return gv, true
	}
	return nil, false
}
