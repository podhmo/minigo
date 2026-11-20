package stdjson

import (
	"encoding/json"
	"fmt"
	"go/ast"
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

// jsonTypIsIface reports whether td is declared as an interface type —
// the Kind flag for resolved typdefs, the AST for name-only ones
// (`interface{}` literal or an `any` spelling).
func jsonTypIsIface(td *runtime.TypeDef) bool {
	if td == nil {
		return false
	}
	if td.Kind == runtime.KindInterface {
		return true
	}
	// ident-spelled interface underlyings (`type T any`, `type E error`)
	// resolve through IfaceBase — reading the spelling instead would
	// misjudge a shadowed `any` (`type any = int`) as an interface.
	if td.IfaceBase {
		return true
	}
	_, ok := td.TypeExpr().(*ast.InterfaceType)
	return ok
}

// jsonIfaceElem reports whether a slice/array typedef's element is
// interface-declared: its stored values are copies, never addressable.
func jsonIfaceElem(c runtime.ElemTyper, td *runtime.TypeDef) bool {
	if td == nil || c == nil {
		return false
	}
	et, err := c.ElemOf(td)
	return err == nil && jsonTypIsIface(et)
}

// jsonReadPathAddr is jsonReadPath plus a flag reporting whether the
// walk crossed pointer storage — a promoted field below a *E embed
// sits in real pointee storage, so it stays addressable even when the
// outer struct marshals by value (gc's CanAddr follows the pointer
// hop). The hop shows in the field value: a pointer embed stores a
// Cell/ref, a value embed stores the Struct itself.
func jsonReadPathAddr(s *runtime.Struct, path []int) (runtime.Value, bool, bool) {
	cur := s
	crossed := false
	for h := 0; h < len(path)-1; h++ {
		f := cur.Fields[path[h]]
		if jsonIsPtrValue(f) {
			crossed = true
		}
		st := runtime.StructOf(f)
		if st == nil {
			return nil, false, false
		}
		cur = st
	}
	return cur.Fields[path[len(path)-1]], true, crossed
}

// jsonIsPtrValue reports whether v is pointer-ish storage — a Cell,
// field/index/deref ref, possibly inside a named tag.
func jsonIsPtrValue(v runtime.Value) bool {
	for {
		switch x := v.(type) {
		case *runtime.Named:
			v = x.V
		case *runtime.Cell, *runtime.FieldRef, *runtime.IndexRef, *runtime.DerefRef:
			return true
		default:
			return false
		}
	}
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
		// the contents of an interface-typed tag are a copy —
		// gc never treats them as addressable
		return goJSONSeen(c, x.V, seen, addr && !jsonTypIsIface(x.Typ))
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
			f, ok, crossed := jsonReadPathAddr(x, e.path)
			if !ok {
				// a nil pointer embed's promoted fields are
				// unreachable — gc omits them entirely
				continue
			}
			if e.omit && jsonIsEmpty(f) {
				continue
			}
			// a field is addressable when the struct is, and also
			// when its promoted path crosses a pointer embed (the
			// pointee is real storage either way); an
			// interface-declared leaf holds a copy, never addressable.
			iface := jsonPathIsIface(c, x.Def, e.path)
			faddr := (addr || crossed) && !iface
			fv := goJSONSeen(c, f, seen, faddr)
			// `,string` applies to the DECLARED kind: interface
			// fields marshal normally even when the tag asks.
			if e.asStr && !iface {
				// a MarshalText result is already the member's content
				// — quoting it like a raw string would double-wrap it.
				_, isStr := fv.(string)
				_, hasText := runtime.IfaceMember(c, jsonMarshalProbe(f, faddr), "MarshalText")
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
		// slice elements marshal through their backing array, which
		// gc always treats as addressable; array elements keep the
		// array's own addressability, and interface-held elements
		// are copies (never addressable).
		eaddr := true
		if x.Typ != nil && stdlib.IsArrayTyp(x.Typ) {
			eaddr = addr
		}
		if jsonIfaceElem(c, x.Typ) {
			eaddr = false
		}
		for i, e := range x.Elems {
			out[i] = goJSONSeen(c, e, seen, eaddr)
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
