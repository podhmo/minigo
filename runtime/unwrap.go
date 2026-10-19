package runtime

import "fmt"

// DeepUnwrap converts a script value, such as a result of
// [github.com/podhmo/minigo.Engine.Call], to plain Go values. Unlike
// [Unwrap] it descends into containers and through pointers, and the
// result can be passed back as a Call argument.
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
//	Cell/refs and Named → the value they point to or wrap, unwrapped
//	nil and the nil-tag values (NIL, IfaceNil, TypedNil) → nil
//
// Everything else — functions, structs, channels, packages — passes
// through as its runtime object.
//
// Cyclic and shared containers are preserved: a container reachable
// twice converts to the same Go slice or map, so a script map that
// stores itself yields a Go map that contains itself.
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

// ErrorOf converts the error result of a script function to a Go error:
// a nil interface gives nil and a [GoValue] holding an error gives that
// error. Any other value gives an error describing the unexpected
// result.
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
