// snapshot.go is SnapshotOf: the value conversion applied to call
// arguments that carry anonymous containers — maps and slices the host
// hands a script (or a script hands a host `any` param) unbox
// element-wise so index/assign works on arrival.
package hostbridge

import (
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// SnapshotOf converts a host Go value into a [VM.Call] argument. The value
// crosses as a snapshot — a copy shaped for the script world — so script
// writes to it never reach the host value. An unnamed host map becomes a
// [runtime.Map] that a map-typed parameter can index, range and write; a
// named map or struct keeps its host box so its methods stay callable.
// Script-native values pass through unchanged.
func SnapshotOf(x any) runtime.Value {
	// The shape is GoValueOf's plus one wider unbox: unnamed maps unbox
	// here while host call results still box them — an arg has a declared
	// param shape to satisfy, a result does not. Named maps and structs
	// keep their box for member dispatch, the rule GoValueOf applies to
	// named slices.
	return snapshotValue(x, map[uintptr]struct{}{})
}

// snapshotValue is SnapshotOf's worker: it threads the set of
// containers currently being unboxed so one that contains itself
// (reachable only through an `any` slot, since a Go map or slice
// type can't name itself anonymously) boxes at the repeat instead of
// recursing forever. Shared subcontainers still unbox — the set
// forgets each container once its elements are built. Arrays skip
// the check: an `any` slot stores an array by copy, so one can never
// reach itself, and a cycle that passes through an array always
// crosses a guarded slice or map on the way back.
func snapshotValue(x any, visiting map[uintptr]struct{}) runtime.Value {
	// the script's int domain is int64: a host int64 stands for a script
	// int, so it enters bare — tagging it (like GoValueOf does for a
	// reflect result) would make `int`-typed params reject the ints
	// callers already pass.
	if v, ok := x.(int64); ok {
		return v
	}
	rv := reflect.ValueOf(x)
	if rv.Kind() == reflect.Map && rv.Type().Name() == "" {
		// recursing through snapshotValue unboxes nested maps too —
		// m["a"]["b"] keeps working at any depth.
		td := HostMapTypOf(rv.Type())
		if rv.IsNil() {
			return &runtime.TypedNil{Typ: td}
		}
		if _, dup := visiting[rv.Pointer()]; dup {
			return &runtime.GoValue{V: x}
		}
		visiting[rv.Pointer()] = struct{}{}
		m := runtime.NewMap(td, rv.Type().Key().Kind() == reflect.Interface)
		for _, k := range rv.MapKeys() {
			m.Insert(snapshotValue(k.Interface(), visiting), snapshotValue(rv.MapIndex(k).Interface(), visiting))
		}
		delete(visiting, rv.Pointer())
		return m
	}
	if (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) && rv.Type().Name() == "" {
		switch rv.Type() {
		case reflect.TypeOf([]byte(nil)), reflect.TypeOf([]string(nil)):
			// the []byte/[]string shapes keep their element-tagged unbox —
			// byte elements arrive tagged uint8, not bare int64s.
		default:
			// every other anonymous slice/array unboxes element-wise.
			// Elements that can carry a nested anonymous container —
			// a map-typed slot, or an `any` slot holding one — route
			// through snapshotValue so `rs[0]["k"]` and `m[k]` index on
			// arrival; primitives keep GoValueOf's tagged shape (an
			// int64 in an `any` slot stays `int64`, not bare `int`).
			// The typedef spells the composite element correctly
			// (`[]map[string]interface{}` — an unparseable one-identifier
			// element name never reaches the param check).
			var td *runtime.TypeDef
			if rv.Kind() == reflect.Slice {
				td = HostSliceTypOf(rv.Type().Elem())
				if rv.IsNil() {
					return &runtime.TypedNil{Typ: td}
				}
				// the same repeat guard the map branch keeps: a slice
				// that holds itself through an `any` slot (s[0] = s)
				// boxes at the repeat instead of overflowing the stack.
				// A len-0 slice can neither recurse nor be a real
				// repeat — s[:0] shares s's base pointer, so it must
				// skip the guard instead of boxing as a false cycle.
				if rv.Len() > 0 {
					if _, dup := visiting[rv.Pointer()]; dup {
						return &runtime.GoValue{V: x}
					}
					visiting[rv.Pointer()] = struct{}{}
					defer delete(visiting, rv.Pointer())
				}
			} else {
				td = HostArrayTypOf(rv.Type())
			}
			et := rv.Type().Elem()
			el := make([]runtime.Value, rv.Len())
			for i := range el {
				ev := rv.Index(i)
				if et.Kind() == reflect.Interface {
					pv := reflect.ValueOf(ev.Interface())
					switch {
					case !pv.IsValid():
						el[i] = runtime.NIL
					case pv.Kind() == reflect.Map || pv.Kind() == reflect.Slice || pv.Kind() == reflect.Array:
						el[i] = snapshotValue(pv.Interface(), visiting)
					default:
						el[i] = GoValueOf(pv)
					}
				} else if et.Kind() == reflect.Map || et.Kind() == reflect.Slice || et.Kind() == reflect.Array {
					el[i] = snapshotValue(ev.Interface(), visiting)
				} else {
					el[i] = GoValueOf(ev)
				}
			}
			return &runtime.Slice{Elems: el, Typ: td}
		}
	}
	return GoValueOf(rv)
}

// argBack pairs an argument with the host value it marshaled to so a
// callee's writes through it can mirror back into the script value.
