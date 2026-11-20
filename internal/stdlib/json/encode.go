package stdjson

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// goJSON marshals a script value into the shape encoding/json expects:
// structs become field-name maps, runtime maps/slices recurse, GoValue
// unwraps. c carries the engine's type-resolution hooks — embedded
// fields promote through them.
func goJSON(c runtime.MemberShapeCaller, v any) any {
	return goJSONSeen(c, v, map[any]bool{}, false)
}

// jsonMarshalProbe spells the value the marshaler probes run on: an
// addressable operand (gc's CanAddr) sees pointer-receiver
// MarshalJSON/MarshalText too, which a Cell wrapper surfaces through
// IfaceMember. A value that already carries a reference shape needs
// no wrapper.
func jsonMarshalProbe(v any, addr bool) any {
	if !addr {
		return v
	}
	switch v.(type) {
	case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		return v
	}
	return &runtime.Cell{Elem: v}
}

// goJSONSeen is goJSON carrying a path set of live container
// pointers: re-encountering one means a reference cycle, which gc
// answers with UnsupportedValueError (a jsonCycle marker carries that
// error out through the host encoder's channel). addr reports whether
// v sits in addressable storage — a field of an addressable struct, a
// slice element, a cell/ref pointee — so pointer-receiver marshalers
// are in scope like gc's CanAddr fields.
func goJSONSeen(c runtime.MemberShapeCaller, v any, seen map[any]bool, addr bool) any {
	// a value whose method set offers MarshalJSON encodes itself —
	// gc's marshalerEncoder calls it instead of walking the value by
	// shape. A nil pointer/interface answers null below without a call
	// (gc's nil-pointer exception).
	switch v.(type) {
	case *runtime.TypedNil, *runtime.IfaceNil:
	case nil:
	default:
		if !jsonNilish(v) {
			pv := jsonMarshalProbe(v, addr)
			if m, ok := runtime.IfaceMember(c, pv, "MarshalJSON"); ok {
				if r, err := c.Call(m, nil); err == nil {
					if b, e, ok := jsonMarshalResult(r); ok {
						if e != nil {
							return &jsonScriptErr{sv: e, method: "MarshalJSON", operand: v}
						}
						return json.RawMessage(b)
					}
				}
			}
			// MarshalText fires only when MarshalJSON does not; its
			// bytes are UTF-8 text re-emitted as a JSON string, not a
			// raw literal like MarshalJSON's output.
			if m, ok := runtime.IfaceMember(c, pv, "MarshalText"); ok {
				if r, err := c.Call(m, nil); err == nil {
					if b, e, ok := jsonMarshalResult(r); ok {
						if e != nil {
							return &jsonScriptErr{sv: e, method: "MarshalText", operand: v}
						}
						return string(b)
					}
				}
			}
		}
	}
	if u, ok := jsonFuncValue(v); ok {
		return u
	}
	switch x := v.(type) {
	case *runtime.Named:
		return goJSONSeen(c, x.V, seen, addr)
	case *runtime.Cell:
		if jsonSeen(seen, x) {
			return jsonCycle{via: typeStrOf(x)}
		}
		defer delete(seen, x)
		return goJSONSeen(c, x.Elem, seen, true)
	case *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
		// a pointer to a field or element (`&s.F`, `&arr[i]`) encodes
		// its pointee like any other pointer — passing the ref through
		// would reflect on its bookkeeping fields. Guard it like a Cell:
		// `s.V = &s.V` revisits the same ref.
		if jsonSeen(seen, x) {
			return jsonCycle{via: typeStrOf(x)}
		}
		defer delete(seen, x)
		if el, ok := runtime.Deref(x); ok {
			return goJSONSeen(c, el, seen, true)
		}
		return nil
	case *runtime.TypedNil, *runtime.IfaceNil:
		// a nil pointer/interface field marshals as null — passing the
		// wrapper through would reflect on its bookkeeping fields.
		return nil
	case *runtime.Struct:
		if jsonSeen(seen, x) {
			// a struct only recurs through a pointer to it — gc names
			// that pointer type.
			return jsonCycle{via: "*" + typeStrOf(x)}
		}
		defer delete(seen, x)
		// structs marshal in DECLARATION order (encoding/json never
		// sorts struct fields) — an ordered map keeps that visible.
		// Anonymous embeds flatten: promoted fields emit in place, a
		// name tag on the embed demotes it to a named member, and a
		// depth/tag tie for one key drops every candidate (gc emits
		// nothing — not even a null — for an ambiguous key).
		var o orderedObject
		ents := jsonMarshalLive(jsonStructFields(c, x.Def))
		for _, e := range ents {
			f, ok := jsonReadPath(x, e.path)
			if !ok {
				// a nil pointer embed's promoted fields are
				// unreachable — gc omits them entirely
				continue
			}
			if e.omit && jsonIsEmpty(f) {
				continue
			}
			fv := goJSONSeen(c, f, seen, addr)
			// `,string` applies to the DECLARED kind: interface
			// fields marshal normally even when the tag asks.
			if e.asStr && !jsonPathIsIface(c, x.Def, e.path) {
				// a MarshalText result is already the member's content
				// — quoting it like a raw string would double-wrap it.
				_, isStr := fv.(string)
				_, hasText := runtime.IfaceMember(c, jsonMarshalProbe(f, addr), "MarshalText")
				if !(isStr && hasText) {
					if lit, ok := jsonStringLit(f); ok {
						fv = lit
					}
				}
			}
			o.keys = append(o.keys, e.key)
			o.vals = append(o.vals, fv)
		}
		return o
	case *runtime.Slice:
		if jsonSeen(seen, x) {
			return jsonCycle{via: typeStrOf(x)}
		}
		defer delete(seen, x)
		out := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			// slice elements marshal through their backing array,
			// which gc always treats as addressable
			out[i] = goJSONSeen(c, e, seen, true)
		}
		return out
	case *runtime.Map:
		if jsonSeen(seen, x) {
			return jsonCycle{via: typeStrOf(x)}
		}
		defer delete(seen, x)
		m := make(map[string]any, x.Len())
		kt := jsonMapKeyTyp(c, x.Typ)
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			kk := jsonMapKeyName(c, k, kt)
			if se, ok := kk.(*jsonScriptErr); ok {
				return se
			}
			// a map value is a copy — never addressable
			m[kk.(string)] = goJSONSeen(c, e, seen, false)
		}
		return m
	case *runtime.GoValue:
		return x.V
	case map[any]any: // hostbridge.GoNative's *runtime.Map output — keys stringify
		if len(x) > 0 {
			if jsonSeen(seen, reflect.ValueOf(x).UnsafePointer()) {
				return jsonCycle{via: fmt.Sprintf("%T", x)}
			}
			defer delete(seen, reflect.ValueOf(x).UnsafePointer())
		}
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[stdlib.Str(k)] = goJSONSeen(c, e, seen, false)
		}
		return m
	case map[string]any: // jsonHostArg's restored container copy
		if len(x) > 0 {
			if jsonSeen(seen, reflect.ValueOf(x).UnsafePointer()) {
				return jsonCycle{via: fmt.Sprintf("%T", x)}
			}
			defer delete(seen, reflect.ValueOf(x).UnsafePointer())
		}
		for k, e := range x {
			x[k] = goJSONSeen(c, e, seen, false)
		}
		return x
	case []any: // hostbridge.GoNative's *runtime.Slice output
		if len(x) > 0 {
			if jsonSeen(seen, reflect.ValueOf(x).UnsafePointer()) {
				return jsonCycle{via: fmt.Sprintf("%T", x)}
			}
			defer delete(seen, reflect.ValueOf(x).UnsafePointer())
		}
		for i, e := range x {
			x[i] = goJSONSeen(c, e, seen, false)
		}
		return x
	default:
		return v
	}
}
