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
//	Map        → map[any]any of unwrapped key/value pairs — keys unwrap
//	             only through the layers that keep comparability (a
//	             script array key stays its comparable box, never an
//	             unhashable []any)
//	GoValue    → the wrapped host value
//	UConst     → its materialized native value — a constant it cannot
//	             represent (like 1<<100) stays a *UConst
//	Spread     → the wrapped slice, unwrapped
//	Cell/refs and Named → the underlying value first
//	nil and the nil-tag values (NIL, IfaceNil, TypedNil) → nil
//
// Everything else — functions, structs, channels, packages — passes
// through as its runtime object.
//
// Cyclic values are legal (a script map can store itself through an
// any slot): a container reachable through itself folds back to the
// host container already being built for it, so the cycle arrives
// intact instead of recursing until the host process overflows — the
// same answer the deepHost marshal gives.
func DeepUnwrap(v Value) any {
	return deepUnwrap(v, NewWalk())
}

// deepUnwrap is DeepUnwrap's worker. The Slice/Map arms run through
// the shared Walk: a container registers its output in seen before
// its elements are walked, so a repeat visit returns the partially
// built container — which completes by the time the top-level caller
// finishes, and which shared (non-cyclic) subtrees fold to as well,
// preserving the input's aliasing.
func deepUnwrap(v Value, w *Walk) any {
	if out, ok := w.Container(v, func(e Value) any { return deepUnwrap(e, w) }, UnwrapKey); ok {
		return out
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
	case *Named:
		return deepUnwrap(x.V, w)
	case *Cell, *FieldRef, *IndexRef, *DerefRef:
		if !w.Peel(v) {
			return v
		}
		d, ok := Deref(v)
		if !ok {
			return v
		}
		return deepUnwrap(d, w)
	case *Tuple:
		if old, ok := w.Seen(x); ok {
			return old
		}
		xs := make([]any, len(x.Elems))
		w.Register(x, xs)
		for i, e := range x.Elems {
			xs[i] = deepUnwrap(e, w)
		}
		return xs
	case *Spread:
		return deepUnwrap(x.S, w)
	case *GoValue:
		return x.V
	case *UConst:
		if nv, err := UConstNative(x); err == nil {
			return deepUnwrap(nv, w)
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
