// Host reflect adapter: wraps a real Go function as a *runtime.BuiltinFunc
// so generated binding tables (minigo gen-intrinsics) and hand-written
// bindings can put ordinary pkg.F members into Bind symbol maps. Arguments
// marshal by value through the same rules intrinsics use (goNative); results
// marshal back through ValueOf. Marshaling is by copy — a wrapped function
// cannot mutate script structures, and a script func passed into a wrapped
// Go func arrives boxed as *GoValue.
package minigo

import (
	"fmt"
	"math"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// WrapFunc adapts a real Go function into a callable runtime value.
// Parameters are filled by marshal-by-copy: an argument converts to the
// declared parameter type when its native form is assignable or convertible
// to it, else the call fails. A trailing error result becomes the call's
// error return; other results marshal through ValueOf, and multiple
// results arrive as a *runtime.Tuple.
func WrapFunc(name string, fn any) *runtime.BuiltinFunc {
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func {
		return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return nil, fmt.Errorf("%s: %T is not a function", name, fn)
		}}
	}
	ft := rv.Type()
	nin := ft.NumIn()
	return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, args []runtime.Value) (result runtime.Value, err error) {
		defer func() {
			if r := recover(); r != nil {
				result, err = nil, fmt.Errorf("%s: %v", name, r)
			}
		}()
		variadic := ft.IsVariadic()
		if (!variadic && len(args) != nin) || (variadic && len(args) < nin-1) {
			return nil, fmt.Errorf("%s: got %d argument(s)", name, len(args))
		}
		in := make([]reflect.Value, len(args))
		for i, a := range args {
			var pt reflect.Type
			if variadic && i >= nin-1 {
				pt = ft.In(nin - 1).Elem()
			} else {
				pt = ft.In(i)
			}
			av, err := toNative(a, pt)
			if err != nil {
				return nil, fmt.Errorf("%s: argument %d: %w", name, i+1, err)
			}
			in[i] = av
		}
		var out []reflect.Value
		if variadic {
			out = rv.CallSlice(packVariadic(ft, in))
		} else {
			out = rv.Call(in)
		}
		// a trailing error-typed result becomes the Go error channel
		if n := len(out); n > 0 && out[n-1].Type() == errorType {
			if !out[n-1].IsNil() {
				return nil, out[n-1].Interface().(error)
			}
			out = out[:n-1]
		}
		switch len(out) {
		case 0:
			return runtime.NIL, nil
		case 1:
			return ValueOf(out[0].Interface()), nil
		default:
			elems := make([]runtime.Value, len(out))
			for i, r := range out {
				elems[i] = ValueOf(r.Interface())
			}
			return &runtime.Tuple{Elems: elems}, nil
		}
	}}
}

// packVariadic rebuilds the argument vector for CallSlice: leading args stay
// as-is and the tail collapses into one []T slice.
func packVariadic(ft reflect.Type, in []reflect.Value) []reflect.Value {
	n := ft.NumIn()
	out := make([]reflect.Value, n)
	copy(out, in[:n-1])
	tail := reflect.MakeSlice(ft.In(n-1), 0, len(in)-n+1)
	for _, v := range in[n-1:] {
		tail = reflect.Append(tail, v)
	}
	out[n-1] = tail
	return out
}

// toNative marshals one runtime value toward parameter type pt: nil fills
// the zero value, assignable natives pass through, convertible ones
// convert (int64 to int, float64 to int, ...), and slices/maps convert
// element-wise — a script []T arrives as []any, so []string-style params
// need their elements converted one at a time.
func toNative(v runtime.Value, pt reflect.Type) (reflect.Value, error) {
	x := goNative(v)
	if x == nil {
		return reflect.Zero(pt), nil
	}
	av := reflect.ValueOf(x)
	if av.Type().AssignableTo(pt) {
		return av, nil
	}
	if av.Type().ConvertibleTo(pt) {
		return av.Convert(pt), nil
	}
	switch pt.Kind() {
	case reflect.Slice:
		if av.Kind() == reflect.Slice {
			out := reflect.MakeSlice(pt, av.Len(), av.Len())
			for i := 0; i < av.Len(); i++ {
				ev, err := nativeElem(av.Index(i), pt.Elem())
				if err != nil {
					return reflect.Value{}, fmt.Errorf("index %d: %w", i, err)
				}
				out.Index(i).Set(ev)
			}
			return out, nil
		}
	case reflect.Map:
		if av.Kind() == reflect.Map {
			out := reflect.MakeMapWithSize(pt, av.Len())
			iter := av.MapRange()
			for iter.Next() {
				kv, err := nativeElem(iter.Key(), pt.Key())
				if err != nil {
					return reflect.Value{}, fmt.Errorf("key: %w", err)
				}
				mv, err := nativeElem(iter.Value(), pt.Elem())
				if err != nil {
					return reflect.Value{}, fmt.Errorf("value: %w", err)
				}
				out.SetMapIndex(kv, mv)
			}
			return out, nil
		}
	}
	if pt.Kind() == reflect.Interface && av.Type().Implements(pt) {
		return av, nil
	}
	return reflect.Value{}, fmt.Errorf("cannot use %T as %s", x, pt)
}

// nativeElem converts one collection element — possibly interface-wrapped
// ([]any members) — toward the declared element type.
func nativeElem(av reflect.Value, pt reflect.Type) (reflect.Value, error) {
	for av.Kind() == reflect.Interface {
		if av.IsNil() {
			return reflect.Zero(pt), nil
		}
		av = av.Elem()
	}
	if !av.IsValid() {
		return reflect.Zero(pt), nil
	}
	if av.Type().AssignableTo(pt) {
		return av, nil
	}
	if av.Type().ConvertibleTo(pt) {
		return av.Convert(pt), nil
	}
	if pt.Kind() == reflect.Interface && av.Type().Implements(pt) {
		return av, nil
	}
	return reflect.Value{}, fmt.Errorf("cannot use %s as %s", av.Type(), pt)
}

// ValueOf marshals a host Go value to its runtime counterpart: script-native
// types and runtime values pass through, numeric kinds widen to int64 /
// float64, and everything else stays boxed as a host GoValue. (Value is an
// `any` alias, so the runtime types must be named explicitly — a
// `case runtime.Value` would swallow every input.)
func ValueOf(x any) runtime.Value {
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
