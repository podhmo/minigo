package hostbridge

import (
	"fmt"

	"github.com/podhmo/minigo/runtime"
)

// Float32Tag reports whether a Named tag's underlying type is float32 —
// the one builtin where a float64 box and the declared width disagree in
// formatting. `type F32 float32` tags count too (Anon names the builtin).
func Float32Tag(td *runtime.TypeDef) bool {
	return runtime.BasicNameOf(td) == "float32"
}

// Complex64Tag mirrors Float32Tag for complex64 — the payload rides the
// float64/complex128 domain but formats at complex64 width per part.
func Complex64Tag(td *runtime.TypeDef) bool {
	return runtime.BasicNameOf(td) == "complex64"
}

// Complex64Value reads a payload as complex64 — either a complex128 box
// or the constant domain's complex literal.
func Complex64Value(v runtime.Value) (complex64, bool) {
	switch pv := v.(type) {
	case complex128:
		return complex64(pv), true
	case *runtime.UConst:
		if nv, err := runtime.UConstNative(pv); err == nil {
			if cv, ok := nv.(complex128); ok {
				return complex64(cv), true
			}
		}
	}
	return 0, false
}

// MustNativeConst is runtime.UConstNative for a builtin argument position that
// cannot report the error: it panics with Go's compile-error wording.
func MustNativeConst(u *runtime.UConst) any {
	nv, err := runtime.UConstNative(u)
	if err != nil {
		panic(&runtime.Panic{Value: &runtime.RuntimeError{Msg: err.Error()}})
	}
	return nv
}

// GoNative converts a runtime value to its Go-native counterpart for host
// calls: cells unwrap, slices/maps become []any / map[any]any, structs get a
// Stringer view so fmt prints them sensibly.
func GoNative(v runtime.Value) any {
	return GoNativeWalk(v, runtime.NewWalk())
}

// GoNativeWalk is GoNative's worker. The Slice/Map arms run through
// the shared runtime.Walk — which, besides the dedupe, gives the walk
// cycle folding (a container holding itself through an any slot used
// to recurse until the stack overflowed), the virtual-slice length
// (a make([]struct{}, n) used to arrive empty), and comparable keys
// (a script array key used to panic "hash of unhashable type").
func GoNativeWalk(v runtime.Value, w *runtime.Walk) any {
	if n, ok := v.(*runtime.Named); ok {
		// fast path for the common int-tagged element (every byte of a
		// []byte argument): only a host-scalar tag changes an int64
		// payload — the float32/complex64 narrowing never applies.
		if iv, ok := n.V.(int64); ok && (n.Typ == nil || n.Typ.HostScalar == nil) {
			return iv
		}
	}
	if out, ok := w.Container(v, func(e runtime.Value) any { return GoNativeWalk(e, w) }, runtime.UnwrapKey); ok {
		return out
	}
	switch x := v.(type) {
	case runtime.Nil:
		return nil
	case *runtime.IfaceNil:
		// a nil interface value crosses to the host as nil (a typed
		// nil stays a TypedNil — the host has no box for it).
		return nil
	case *runtime.UConst:
		return MustNativeConst(x)
	case *runtime.Named:
		// a float32-tagged value crosses to fmt as a real float32 so
		// %v applies float32 formatting (0.1, not 0.10000000149011612);
		// %T is rewritten to scriptTypeString before this runs.
		if Float32Tag(x.Typ) {
			if fv, ok := x.V.(float64); ok {
				return float32(fv)
			}
			// the payload may still ride the constant domain — a
			// `float32(c)` conversion keeps UConst so the value stays
			// arbitrary-precision; read it at float32 width the same
			// way the materialized float64 path above does.
			if uc, ok := x.V.(*runtime.UConst); ok {
				if nv, err := runtime.UConstNative(uc); err == nil {
					if fv, ok := nv.(float64); ok {
						return float32(fv)
					}
				}
			}
		}
		// a complex64-tagged value narrows each part to float32 — the
		// constant domain's widened digits must not reach the host.
		if Complex64Tag(x.Typ) {
			if cv, ok := Complex64Value(x.V); ok {
				return cv
			}
		}
		// a bound host scalar crossing to a host `any` parameter
		// materializes to the host type — a host call sees a real
		// time.Month, not the int64 or UConst the script shape keeps.
		if x.Typ != nil && x.Typ.HostScalar != nil {
			if hv, ok := runtime.HostScalarOf(x.Typ, x.V); ok {
				return hv
			}
		}
		return GoNativeWalk(x.V, w)
	case *runtime.Cell:
		return GoNativeWalk(x.Elem, w)
	case *runtime.Struct:
		return fmt.Sprintf("%s%+v", x.Def.Name, x.Fields)
	case *runtime.GoValue:
		return x.V
	default:
		return v
	}
}
