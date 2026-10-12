package runtime

import "fmt"

// DeepUnwrap converts a script value to its host-side Go shape — the
// recursive answer to "what did the script hand me?" for Engine.Call
// results. Where Unwrap peels one Named tag, DeepUnwrap walks every
// wrapper layer so nested values arrive unboxed too, and the result can
// be fed back as a Call argument (host maps and slices re-box as the
// anonymous script shapes on the way in).
//
//	Tuple      → []any of unwrapped elements
//	Slice      → []any of unwrapped elements (a virtual slice expands
//	             its zero fill to the logical length)
//	Map        → map[any]any of unwrapped key/value pairs
//	GoValue    → the wrapped host value
//	UConst     → its materialized native value — a constant it cannot
//	             represent (like 1<<100) stays a *UConst
//	Spread     → the wrapped slice, unwrapped
//	Cell/refs and Named → the underlying value first
//	nil and the nil-tag values (NIL, IfaceNil, TypedNil) → nil
//
// Everything else — functions, structs, channels, packages — passes
// through as its runtime object.
func DeepUnwrap(v Value) any {
	for {
		if n, ok := v.(*Named); ok {
			v = n.V
			continue
		}
		if d, ok := Deref(v); ok {
			v = d
			continue
		}
		break
	}
	switch x := v.(type) {
	case nil, Nil:
		return nil
	case *IfaceNil:
		// host code has no shape for "interface holding a typed nil" —
		// the nil is the honest answer either way.
		return nil
	case *TypedNil:
		return nil
	case *Tuple:
		xs := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			xs[i] = DeepUnwrap(e)
		}
		return xs
	case *Slice:
		n := int(x.Len())
		xs := make([]any, n)
		if x.Virtual() {
			zero := DeepUnwrap(x.Zero)
			for i := range xs {
				xs[i] = zero
			}
			return xs
		}
		for i, e := range x.Elems {
			xs[i] = DeepUnwrap(e)
		}
		return xs
	case *Map:
		m := make(map[any]any, x.Len())
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			m[DeepUnwrap(k)] = DeepUnwrap(e)
		}
		return m
	case *Spread:
		return DeepUnwrap(x.S)
	case *GoValue:
		return x.V
	case *UConst:
		if nv, err := UConstNative(x); err == nil {
			return DeepUnwrap(nv)
		}
		return x
	}
	return v
}

// ErrorOf unwraps a script-returned error slot — the `(T, error)`
// result contract Engine.Call surfaces — where nil stays nil, a
// nil-tagged interface result is a nil error, and a boxed host error
// surfaces as the call's error. Anything else is a contract violation:
// the slot's shape only carries nil or a host error, so an unexpected
// shape reports the bug rather than silently degrading.
func ErrorOf(v Value) error {
	e := Unwrap(v)
	if e == nil || e == NIL {
		return nil
	}
	if IfaceTaggedNil(e) != nil {
		return nil
	}
	if gv, ok := e.(*GoValue); ok {
		if err, ok := gv.V.(error); ok {
			return err
		}
	}
	return fmt.Errorf("minigo: unexpected error result %T", v)
}
