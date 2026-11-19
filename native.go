// Host reflect adapter: wraps a real Go function as a *runtime.BuiltinFunc
// so generated binding tables (minigo gen-intrinsics) and hand-written
// bindings can put ordinary pkg.F members into Bind symbol maps. Arguments
// marshal by value through the same rules intrinsics use (hostbridge.GoNative); results
// marshal back through ValueOf. Marshaling is by copy — a wrapped function
// cannot mutate script structures, and a script func passed into a wrapped
// Go func arrives boxed as *GoValue.
//
// The marshal machinery lives in internal/stdlib (P2-12b) so the moved
// stdlib installers under internal/stdlib/<path> can share it; the root
// package keeps these thin wrappers for the existing call sites.

package minigo

import (
	"math"
	"reflect"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// WrapFunc makes a Go function callable from scripts, typically as a
// symbol for [Engine.Bind]. Arguments are copied: each converts to the
// declared parameter type when assignable or convertible, else the call
// fails, and fn cannot mutate script values. A trailing error result
// becomes the call's error; other results convert with [ValueOf], and
// multiple results arrive as a [*runtime.Tuple].
func WrapFunc(name string, fn any) *runtime.BuiltinFunc {
	return stdlib.WrapFuncWith(name, fn, ValueOf, false)
}

// wrapBind is WrapFunc whose results ride the host→script conversion the
// handwritten intrinsics use (scriptVal).
func wrapBind(name string, fn any) *runtime.BuiltinFunc {
	return stdlib.WrapBind(name, fn)
}

// composite literal of the bound type (store.Issue{ID: 7}) then builds
// a *T through reflection: keyed fields must name exported fields and
// each value converts to the field's type, and the result is a genuine
// *store.Issue — bound reflect-driven code (html/template,
// encoding/json) sees its fields, and the value passes back into the
// bound package's own functions. `var x T` mints the same host zero.
//
// A bound type declared without it — the shape `minigo gen-intrinsics`
// emits for non-struct types — carries no field knowledge: a keyed
// literal traps "has no field", and a value its package hands out stays
// an opaque box. Where that shape is unavoidable, keep the
// value-producing package source-interpreted ([WithPackageModes]
// ModeSource): script-declared structs marshal into bound templates
// field-by-field already.
func HostTypeOf(x any) *runtime.TypeDef {
	rt := reflect.TypeOf(x)
	if rt == nil {
		panic("minigo.HostTypeOf: nil value")
	}
	for rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt.Name() == "" {
		panic("minigo.HostTypeOf: " + rt.String() + " is not a named type")
	}
	return &runtime.TypeDef{
		Name:    rt.String(),
		Kind:    runtime.KindStruct,
		HostNew: func() any { return reflect.New(rt).Interface() },
	}
}

// ValueOf converts x for use in scripts: runtime values pass through,
// numeric kinds widen to int64 or float64, and everything else is boxed as
// a [*runtime.GoValue] whose methods scripts can call.
func ValueOf(x any) runtime.Value {
	// runtime.Value is an `any` alias, so the runtime types must be named
	// explicitly — a `case runtime.Value` would swallow every input.
	switch v := x.(type) {
	case nil:
		return runtime.NIL
	case int, int8, int16, int32, int64:
		return reflect.ValueOf(x).Int()
	case uint, uint8, uint16, uint32, uint64:
		u := reflect.ValueOf(x).Uint()
		if u <= math.MaxInt64 {
			return int64(u)
		}
		return &runtime.GoValue{V: x} // no script-wide uint64; keep the value exact
	case float32, float64:
		return reflect.ValueOf(x).Float()
	case bool, string:
		return x
	case runtime.Nil, *runtime.Tuple, *runtime.Cell, *runtime.Slice,
		*runtime.Map, *runtime.Struct, *runtime.Function, *runtime.Closure,
		*runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.GoValue,
		*runtime.Chan, *runtime.TypeDef, *runtime.Iterator, *runtime.Package,
		*runtime.ImportRef, *runtime.TypedNil, *runtime.IfaceNil,
		*runtime.Named:
		return v
	default:
		return &runtime.GoValue{V: x}
	}
}
