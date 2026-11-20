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
		// gc encodes a 32-bit float at its own precision — re-tag the
		// payload as float32 instead of widening to float64.
		if f, ok := x.V.(float64); ok && jsonUnderlyingName(x.Typ) == "float32" {
			return float32(f)
		}
		// unsigned basics store int64 internally; a decode that fit a
		// uint64 through ParseUint (>MaxInt64) reads back negative —
		// re-read the payload through the unsigned tag before emitting.
		if i, ok := x.V.(int64); ok && i < 0 {
			if un := jsonUnderlyingName(x.Typ); jsonIntName(un) {
				if _, signed := jsonIntWidth(un); !signed {
					return goJSONSeen(c, uint64(i), seen, addr && !jsonTypIsIface(x.Typ))
				}
			}
		}
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
		// a []byte marshals as a base64 string — named element types
		// count too ([]MyByte), while an [N]byte array stays
		// element-wise. An element type implementing a marshaler
		// demotes the slice to the element-wise path like gc.
		if x.Typ != nil && !stdlib.IsArrayTyp(x.Typ) && jsonSliceIsByte(c, x.Typ) && !jsonElemMarshals(c, x.Elems) {
			out := make([]byte, len(x.Elems))
			for i, e := range x.Elems {
				u, _ := runtime.Unwrap(e).(int64)
				out[i] = byte(u)
			}
			return out
		}
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

// jsonSliceIsByte reports whether td's element type is byte/uint8 —
// a named byte typedef counts too, matching gc's `[]MyByte` base64
// rule. The AST `byte`/`uint8` spelling alone is not enough: a
// scope may shadow `byte` (`type byte string`), so the element
// resolves to its typedef and the UNDERLYING name decides.
func jsonSliceIsByte(c runtime.MemberShapeCaller, td *runtime.TypeDef) bool {
	et := td.Elem
	if et == nil && c != nil {
		// a named element type resolves through the caller —
		// `[]MyByte` spells its elem "main.MyByte", not "uint8".
		if r, err := c.ElemOf(td); err == nil {
			et = r
		}
	}
	if et == nil {
		return false
	}
	un := jsonUnderlyingName(et)
	return un == "byte" || un == "uint8"
}

// jsonElemMarshals reports whether the slice's element type carries
// MarshalJSON/MarshalText — gc skips the []byte base64 shortcut
// then. Every element shares one static type, so one probe decides;
// slice elements are addressable, so pointer receivers count too.
func jsonElemMarshals(c runtime.MemberShapeCaller, elems []runtime.Value) bool {
	if len(elems) == 0 {
		return false
	}
	pv := jsonMarshalProbe(elems[0], true)
	if m, ok := runtime.IfaceMember(c, pv, "MarshalJSON"); ok && jsonMarshalerMember(m) {
		return true
	}
	m, ok := runtime.IfaceMember(c, pv, "MarshalText")
	return ok && jsonMarshalerMember(m)
}

// jsonMarshalerMember reports whether the member m is a method
// callable as `func() ([]byte, error)` — gc's signature for both
// json.Marshaler and encoding.TextMarshaler. A same-named method
// with another signature does not satisfy the interface, so the
// base64 shortcut stays on (a `MarshalJSON() string` changes
// nothing under gc).
func jsonMarshalerMember(m runtime.Value) bool {
	switch fn := m.(type) {
	case *runtime.BoundMethod:
		if fn.Fn == nil || fn.Fn.Decl == nil {
			return false
		}
		return jsonMarshalerFuncType(fn.Fn.Decl.Type)
	case *runtime.BuiltinFunc:
		// the reflected method's signature carries its receiver as
		// In(0); the bound call must return ([]byte, error).
		if fn.Method == nil {
			return false
		}
		mt := fn.Method.Type
		return mt.NumIn() == 1 && mt.NumOut() == 2 &&
			mt.Out(0) == reflect.TypeOf([]byte(nil)) &&
			mt.Out(1) == reflect.TypeOf((*error)(nil)).Elem()
	}
	return false
}

// jsonMarshalerFuncType reports whether a declared method type
// spells `func() ([]byte, error)`: no parameters (the BoundMethod's
// decl carries no receiver) and exactly two results spelled
// `[]byte` and `error`.
func jsonMarshalerFuncType(ft *ast.FuncType) bool {
	if ft == nil {
		return false
	}
	if ft.Params != nil && len(ft.Params.List) > 0 {
		return false
	}
	if ft.Results == nil || len(ft.Results.List) != 2 {
		return false
	}
	// each result must be a single name or unnamed — `([]byte, b)`
	// shapes cannot spell the marshaler signature.
	for _, r := range ft.Results.List {
		if len(r.Names) > 1 {
			return false
		}
	}
	if at, ok := ft.Results.List[0].Type.(*ast.ArrayType); !ok || at.Len != nil || !jsonByteIdent(at.Elt) {
		return false
	}
	id, ok := ft.Results.List[1].Type.(*ast.Ident)
	return ok && id.Name == "error"
}

// jsonByteIdent reports whether e spells the predeclared byte or
// uint8 element of a `[]byte` result.
func jsonByteIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && (id.Name == "byte" || id.Name == "uint8")
}
