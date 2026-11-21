package hostbridge

import (
	"math"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// ConstNative materializes a builtin-argument UConst at its default
// type; any other value passes through unchanged. An unmaterializable
// constant is a program failure, so it panics like intOf.
func ConstNative(v runtime.Value) runtime.Value {
	if u, ok := v.(*runtime.UConst); ok {
		nv, err := runtime.UConstNative(u)
		if err != nil {
			panic(&runtime.Panic{Value: &runtime.RuntimeError{Msg: err.Error()}})
		}
		return nv
	}
	return v
}

// ConstAtWidth materializes a Named value's still-constant payload at
// its declared type, when the caller can run the VM-side conversion —
// display and fmtArg share it so a UConst never spills its untyped
// reading into output. ok is false when x holds no UConst, the caller
// can't materialize, or the conversion fails.
func ConstAtWidth(mc runtime.ConstMaterializer, x *runtime.Named) (runtime.Value, bool) {
	u, ok := x.V.(*runtime.UConst)
	if !ok {
		return nil, false
	}
	if mc == nil {
		return nil, false
	}
	mv, err := mc.MaterializeConstErr(u, x.Typ)
	if err != nil {
		return nil, false
	}
	return mv, true
}

// MakeHostType is make(T, n, cap) of a bound host slice or map type
// (net.IP, url.Values). ok is false for any other type.
func MakeHostType(td *runtime.TypeDef, args []runtime.Value) (runtime.Value, bool) {
	if td.HostNew == nil {
		return nil, false
	}
	rt := td.HostType()
	if rt == nil {
		return nil, false
	}
	switch rt.Kind() {
	case reflect.Slice:
		n := int64(0)
		if len(args) > 1 {
			n = argSize(runtime.Unwrap(ConstNative(args[1])))
		}
		c := n
		if len(args) > 2 {
			c = argSize(runtime.Unwrap(ConstNative(args[2])))
		}
		if n < 0 || n > math.MaxInt32 {
			panic(runtime.MakeslicePanic("len"))
		}
		if c < n || c > math.MaxInt32 {
			panic(runtime.MakeslicePanic("cap"))
		}
		return runtime.Tag(td, &runtime.GoValue{V: reflect.MakeSlice(rt, int(n), int(c)).Interface()}), true
	case reflect.Map:
		return runtime.Tag(td, &runtime.GoValue{V: reflect.MakeMap(rt).Interface()}), true
	}
	return nil, false
}

// argSize reads a make() size argument after constant materialization:
// a native or a defined numeric value still carrying its integer.
func argSize(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case uint64:
		return int64(x)
	case uint, uint8, uint16, uint32, uintptr:
		return int64(reflect.ValueOf(x).Uint())
	case *runtime.UConst:
		// a typed constant `make(net.IP, uint8(4))` unwraps to its
		// UConst payload; materialize like intOf does.
		return argSize(MustNativeConst(x))
	case *runtime.GoValue:
		return argSize(x.V)
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int64(rv.Uint())
	}
	return 0
}

// Int64Of reads a builtin argument's int64 value after constant and
// box unwrapping — the helper make(len)/append/clear positions share.
func Int64Of(v any) int64 {
	return int64(IntOf(v))
}

// IntOf reads a builtin argument's native int value: Named peels,
// UConst materializes at its default type, boxed host scalars unwrap,
// and a defined numeric type still carries its integer value.
func IntOf(v any) int {
	switch x := v.(type) {
	case *runtime.Named:
		return IntOf(x.V)
	case *runtime.Cell:
		return IntOf(x.Elem)
	case *runtime.UConst:
		return IntOf(MustNativeConst(x))
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	case uint64:
		return int(x)
	case uint, uint8, uint16, uint32, uintptr:
		return int(reflect.ValueOf(x).Uint())
	case *runtime.GoValue:
		return IntOf(x.V)
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int(rv.Uint())
	}
	return 0
}
