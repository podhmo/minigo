// argas.go lifts a script argument for a `... any` (or generic) host
// parameter through the ArgAsHooks the calling VM supplies: the VM keeps
// the frame, typedef resolution and trap reporting, hostbridge keeps
// the per-kind lifting rules.
package hostbridge

import (
	"go/ast"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// ArgAsHooks are the VM-side operations Lift needs. The vm package
// builds them per call so the closures capture the current frame.
type ArgAsHooks struct {
	// PeelNamed unwraps a `type T U` typedef to its underlying U.
	PeelNamed func(td *runtime.TypeDef) *runtime.TypeDef
	// ElemOf resolves a container typedef's element typedef.
	ElemOf func(td *runtime.TypeDef) *runtime.TypeDef
	// MapKeyOf resolves a map typedef's key typedef.
	MapKeyOf func(td *runtime.TypeDef) *runtime.TypeDef
	// ArrayLen reports a typedef's array length when it is an array.
	ArrayLen func(td *runtime.TypeDef) (int64, bool)
	// Coerce converts a value at a typedef for the arg slot.
	Coerce func(x runtime.Value, td *runtime.TypeDef) runtime.Value
	// Trap reports a type error like a frame trap.
	Trap func(format string, args ...any)
	// TypeNameOf spells a value's type for trap messages.
	TypeNameOf func(x runtime.Value) string
	// TdName spells a typedef for trap messages.
	TdName func(td *runtime.TypeDef) string
}

// hostArgAs lifts x, an argument that crossed the host as `any`, to the
// value of the script type td a parameter declares. A script container
// reached the host as the generic []any / map[any]any projection (see
// deepHost), which a `[]Pt` or `map[string]Pt` parameter rejects; here
// its elements convert to the declared element type and the container
// takes the declared type, recursively. Whatever does not fit traps, as
// coerce does for a callee's own parameter binding.
func Lift(x runtime.Value, td *runtime.TypeDef, h *ArgAsHooks, depth int) runtime.Value {
	utd := h.PeelNamed(td)
	if utd == nil || depth > 8 {
		return x
	}
	// a host-shaped container comes back boxed: an unnamed host slice
	// GoValueOf unboxes into *runtime.Slice; a host map keeps its
	// GoValue box, so unbox its pairs by hand — the switch below lifts
	// each element to the declared type.
	if gv, ok := x.(*runtime.GoValue); ok {
		if rv := reflect.ValueOf(gv.V); rv.IsValid() {
			switch rv.Kind() {
			case reflect.Slice, reflect.Array:
				x = GoValueOf(rv)
			case reflect.Map:
				// unnamed host maps only: a named map type (url.Values)
				// keeps its GoValue box or its methods vanish. MapRange,
				// not MapKeys+MapIndex — a NaN key's value is
				// unreachable by lookup.
				if !rv.IsNil() && rv.Type().Name() == "" {
					m := runtime.NewMap(nil, rv.Type().Key().Kind() == reflect.Interface)
					it := rv.MapRange()
					for it.Next() {
						m.Insert(GoValueOf(it.Key()), GoValueOf(it.Value()))
					}
					x = m
				}
			}
		}
	}
	switch utd.Kind {
	case runtime.KindSlice:
		switch s := x.(type) {
		case *runtime.TypedNil:
			if HostGenericTyp(s.Typ) {
				x = &runtime.TypedNil{Typ: td}
			}
		case *runtime.Slice:
			// a host-projected []T (Host set: `buf.Bytes()`, a FuncMap
			// round-trip) carries untagged elements like []any does —
			// lift them to the declared element type.
			if et := h.ElemOf(utd); et != nil && !s.Virtual() && (HostGenericTyp(s.Typ) || s.Host.IsValid()) {
				// an array parameter takes only a value of its length
				if n, isArr := h.ArrayLen(utd); isArr && n != int64(len(s.Elems)) {
					h.Trap("cannot use %s as %s", h.TypeNameOf(x), h.TdName(td))
				}
				elems := make([]runtime.Value, len(s.Elems))
				for i, e := range s.Elems {
					elems[i] = Lift(e, et, h, depth+1)
				}
				x = &runtime.Slice{Elems: elems}
			}
		}
	case runtime.KindMap:
		switch m := x.(type) {
		case *runtime.TypedNil:
			if HostGenericTyp(m.Typ) {
				x = &runtime.TypedNil{Typ: td}
			}
		case *runtime.Map:
			kt, et := h.MapKeyOf(utd), h.ElemOf(utd)
			if kt != nil && et != nil && (m.Typ == nil || HostGenericTyp(m.Typ)) {
				ku := h.PeelNamed(kt)
				out := runtime.NewMap(nil, ku != nil && ku.Kind == runtime.KindInterface)
				for i := 0; i < m.Len(); i++ {
					k, e := m.At(i)
					out.Insert(Lift(k, kt, h, depth+1), Lift(e, et, h, depth+1))
				}
				x = out
			}
		}
	}
	r := h.Coerce(x, td)
	if _, ok := r.(*runtime.Struct); ok {
		// coerce copied the struct; an element of a slice the callee
		// writes through must stay the caller's own, as in Go (the
		// callee's parameter binding copies it again where Go would).
		if _, ok := x.(*runtime.Struct); ok {
			return x
		}
	}
	return r
}

// hostGenericTyp reports whether td is the type a generic host
// projection carries — []any or map[any]any, or none at all — the shapes
// hostArgAs lifts (a script value of a declared type is left to coerce).
func HostGenericTyp(td *runtime.TypeDef) bool {
	if td == nil {
		return true
	}
	if td.Name != "" {
		return false
	}
	isAny := func(e ast.Expr) bool {
		switch t := e.(type) {
		case *ast.Ident:
			return t.Name == "any"
		case *ast.InterfaceType:
			return t.Methods == nil || len(t.Methods.List) == 0
		}
		return false
	}
	switch t := td.Anon.(type) {
	case *ast.ArrayType:
		return t.Len == nil && isAny(t.Elt)
	case *ast.MapType:
		return isAny(t.Key) && isAny(t.Value)
	}
	return false
}
