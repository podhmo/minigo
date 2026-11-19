// Host reflect adapter: wraps a real Go function as a *runtime.BuiltinFunc
// so generated binding tables (minigo gen-intrinsics) and hand-written
// bindings can put ordinary pkg.F members into Bind symbol maps. Arguments
// marshal by value through the same rules intrinsics use (hostbridge.GoNative); results
// marshal back through ValueOf. Marshaling is by copy — a wrapped function
// cannot mutate script structures, and a script func passed into a wrapped
// Go func arrives boxed as *GoValue.

package minigo

import (
	"fmt"
	"go/constant"
	"math"
	"reflect"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// WrapFunc makes a Go function callable from scripts, typically as a
// symbol for [Engine.Bind]. Arguments are copied: each converts to the
// declared parameter type when assignable or convertible, else the call
// fails, and fn cannot mutate script values. A trailing error result
// becomes the call's error; other results convert with [ValueOf], and
// multiple results arrive as a [*runtime.Tuple].
func WrapFunc(name string, fn any) *runtime.BuiltinFunc {
	return wrapFuncWith(name, fn, ValueOf, false)
}

// wrapBind is WrapFunc whose results ride the host→script conversion the
// handwritten intrinsics use (scriptVal): slices arrive as
// [*runtime.Slice] and sized ints keep their named width, so a generated
// bound table agrees with the source-interpreted result shapes. Its
// trailing error stays a script-visible value (errVal, like retErr2)
// instead of the Go error channel: `n, err := strconv.Atoi(s)` reads
// two results, as the source-interpreted signature does.
func wrapBind(name string, fn any) *runtime.BuiltinFunc {
	return wrapFuncWith(name, fn, scriptVal, true)
}

func wrapFuncWith(name string, fn any, box func(any) runtime.Value, keepErr bool) *runtime.BuiltinFunc {
	rv := reflect.ValueOf(fn)
	if rv.Kind() != reflect.Func {
		return &runtime.BuiltinFunc{Name: name, Fn: func(_ runtime.VMCaller, _ []runtime.Value) (runtime.Value, error) {
			return nil, fmt.Errorf("%s: %T is not a function", name, fn)
		}}
	}
	ft := rv.Type()
	nin := ft.NumIn()
	return &runtime.BuiltinFunc{Name: name, Target: fn, Fn: func(_ runtime.VMCaller, args []runtime.Value) (runtime.Value, error) {
		// no recover(): a stdlib panic must reach the VM so a script
		// recover() sees it — the same contract h.fn bindings keep.
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
		// a trailing error-typed result becomes the Go error channel, or
		// a script error value when the script sees it in the results
		if n := len(out); n > 0 && out[n-1].Type() == errorType && !keepErr {
			if !out[n-1].IsNil() {
				return nil, out[n-1].Interface().(error)
			}
			out = out[:n-1]
		}
		boxElem := func(r reflect.Value) runtime.Value {
			if keepErr && r.Type() == errorType {
				var e error
				if !r.IsNil() {
					e = r.Interface().(error)
				}
				return errVal(e)
			}
			return box(r.Interface())
		}
		switch len(out) {
		case 0:
			return runtime.NIL, nil
		case 1:
			return boxElem(out[0]), nil
		default:
			elems := make([]runtime.Value, len(out))
			for i, r := range out {
				elems[i] = boxElem(r)
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
	// a constant-domain argument converts to the declared parameter type
	// with Go's own range check — hostbridge.GoNative would either fail to express
	// it at all (1<<63 as int64) or narrow it at int width first.
	if uc, ok := v.(*runtime.UConst); ok {
		if rv, err := uconstToReflect(uc, pt); err != nil || rv.IsValid() {
			return rv, err
		}
	}
	// a typed nil fills the declared type's zero: `AppendInt(nil, ...)`
	// spells the nil []byte as *runtime.TypedNil, not runtime.NIL.
	if _, ok := v.(*runtime.TypedNil); ok {
		switch pt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Chan,
			reflect.Func, reflect.Interface, reflect.UnsafePointer:
			return reflect.Zero(pt), nil
		}
	}
	x := hostbridge.GoNative(v)
	if x == nil {
		return reflect.Zero(pt), nil
	}
	return nativeValue(x, pt)
}

// nativeValue marshals one already-unwrapped Go value toward parameter
// type pt — the body of toNative, shared with collection-element
// conversions so a [][]byte or [4]byte argument converts recursively.
func nativeValue(x any, pt reflect.Type) (reflect.Value, error) {
	av := reflect.ValueOf(x)
	if av.Type().AssignableTo(pt) {
		return av, nil
	}
	// a HostTypeOf literal boxes *T where gc spells T: a by-value
	// parameter wants the pointee (the same rule toReflectValue
	// applies inside fields and elements).
	if av.Kind() == reflect.Pointer && !av.IsNil() && av.Type().Elem().AssignableTo(pt) {
		return av.Elem(), nil
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
	case reflect.Array:
		// a fixed-size parameter (netip.AddrFrom4's [4]byte) takes a
		// same-length script array, converted element-wise.
		if av.Kind() == reflect.Slice && av.Len() == pt.Len() {
			out := reflect.New(pt).Elem()
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

// uconstToReflect converts a constant-domain argument to the declared
// parameter type with Go's range check: an untyped int that fits goes
// through at the parameter's width, one that does not reports the same
// "constant N overflows T" a declared-type conversion would. An
// inapplicable conversion returns the zero Value so the caller falls
// back to the ordinary path.
func uconstToReflect(u *runtime.UConst, pt reflect.Type) (reflect.Value, error) {
	c := u.V
	ci := constant.ToInt(c)
	if ci.Kind() != constant.Int {
		return reflect.Value{}, nil // not an int const — the ordinary path handles it
	}
	rv := reflect.New(pt).Elem()
	if u.Wrap {
		// a non-constant shift result truncates to t's width: the
		// reflect setters store the low bits.
		w := runtime.WrapInt64(ci)
		switch pt.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			rv.SetInt(w)
		default:
			rv.SetUint(uint64(w))
		}
		return rv, nil
	}
	switch pt.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, exact := constant.Int64Val(ci)
		if !exact || rv.OverflowInt(i) {
			return reflect.Value{}, fmt.Errorf("constant %s overflows %s", c.ExactString(), pt)
		}
		rv.SetInt(i)
		return rv, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n, exact := constant.Uint64Val(ci)
		if !exact || rv.OverflowUint(n) {
			return reflect.Value{}, fmt.Errorf("constant %s overflows %s", c.ExactString(), pt)
		}
		rv.SetUint(n)
		return rv, nil
	}
	return reflect.Value{}, nil
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
	// the element marshals through the same rules as an argument, so a
	// nested collection ([][]byte members of a Join argument) converts.
	return nativeValue(av.Interface(), pt)
}

// HostTypeOf returns the typedef [Engine.Bind] needs for a named host
// type whose literals must mint real host values — pass a zero value of
// the type (HostTypeOf(store.Issue{})) or a pointer to it. A script
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
