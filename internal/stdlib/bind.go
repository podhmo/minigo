// Package stdlib hosts the shared binding machinery and the moved
// per-package stdlib installers (P2-12b): generated bound tables live in
// internal/stdlib/<path> and register through Install.
package stdlib

import (
	"fmt"
	"go/ast"
	"go/constant"
	"reflect"
	"time"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// Bind registers one lazily built stdlib table — the shape
// Engine.bindBound has, passed in from the registration site.
type Bind func(path string, symbols map[string]runtime.Value)

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// WrapBind is WrapFunc whose results ride the host→script conversion the
// handwritten intrinsics use (ScriptVal): slices arrive as
// [*runtime.Slice] and sized ints keep their named width, so a generated
// bound table agrees with the source-interpreted result shapes. Its
// trailing error stays a script-visible value (ErrVal, like retErr2)
// instead of the Go error channel: `n, err := strconv.Atoi(s)` reads
// two results, as the source-interpreted signature does.
func WrapBind(name string, fn any) *runtime.BuiltinFunc {
	return WrapFuncWith(name, fn, ScriptVal, true)
}

func WrapFuncWith(name string, fn any, box func(any) runtime.Value, keepErr bool) *runtime.BuiltinFunc {
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
				return ErrVal(e)
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

// HostType is a TypeDef whose zero is a host value: `var m T` and `T{}`
// produce a boxed *newT() so selectMember sees the real method set.
func HostType(name string, new func() any) *runtime.TypeDef {
	return &runtime.TypeDef{Name: name, Kind: runtime.KindStruct, HostNew: new}
}

func ErrVal(err error) runtime.Value {
	if err == nil {
		return runtime.NIL
	}
	if sb, ok := err.(runtime.ScriptBoxed); ok {
		// a script error that went through host code (a %w cause, a
		// cancel cause, a callback's result) reads back as itself.
		return sb.ScriptValue()
	}
	return &runtime.GoValue{V: err} // boxed: method calls (Error(), Unwrap()) dispatch via reflection
}

// NamedSized boxes a host-sized int with its declared typedef so %T
// spells int8/int16/int32/int64/uintN like Go (rune is int32).
func NamedSized(x any, v int64) runtime.Value {
	return runtime.Tag(runtime.BasicTypedef(fmt.Sprintf("%T", x)), v)
}

// ScriptVal converts a Go-native result back to a runtime value. Concrete
// runtime types pass through; anything else (errors, host structs) is boxed
// as a GoValue — Value is `any`, so it cannot be a type-switch case itself.
func ScriptVal(v any) runtime.Value {
	switch x := v.(type) {
	case nil:
		return runtime.NIL
	case bool, string, float64:
		return x
	case int64:
		// int64 is a named type in Go — distinct from int at assert time —
		// so host int64 results ride in the box like the other sized ints.
		return NamedSized(x, x)
	case int:
		return int64(x)
	case int8, int16, int32:
		return NamedSized(x, reflect.ValueOf(x).Int())
	case uint, uint8, uint16, uint32, uint64, uintptr:
		// int64(x) keeps the full bit pattern — a >=2^63 uint64 like
		// math.Float64bits(-0.0) reinterprets losslessly.
		return NamedSized(x, int64(reflect.ValueOf(x).Uint()))
	case float32:
		// keep the declared width like a float32(x) conversion does —
		// equality and map keys need the float32 tag, the payload rides
		// in the float64 domain.
		return runtime.Tag(runtime.BasicTypedef("float32"), float64(x))
	case []byte:
		if x == nil {
			return &runtime.TypedNil{Typ: hostbridge.AnonSliceTyp("byte")}
		}
		// a []byte result unmarshals to byte-tagged elements so
		// `string(b)` and indexing behave like Go source suggests, and
		// elements compare like a []byte{...} literal's.
		el := make([]runtime.Value, len(x))
		for i, b := range x {
			el[i] = runtime.ByteValue(b)
		}
		return &runtime.Slice{Elems: el, Typ: hostbridge.AnonSliceTyp("byte")}
	case []string:
		return StrsSlice(x)
	case []rune:
		return RuneSlice(x)
	case [][]byte:
		return ScriptVal(BytesSliceOf(x))
	case time.Duration:
		// durations stay raw host values: methods (.Hours(), .String())
		// dispatch through reflection and binaryOp unwraps for arithmetic.
		return x
	case []any:
		if x == nil {
			return &runtime.TypedNil{Typ: hostbridge.AnonSliceTyp("any")}
		}
		el := make([]runtime.Value, len(x))
		for i, e := range x {
			el[i] = ScriptVal(e)
		}
		return &runtime.Slice{Elems: el, Typ: hostbridge.AnonSliceTyp("any")}
	case map[any]any:
		if x == nil {
			return &runtime.TypedNil{Typ: &runtime.TypeDef{Kind: runtime.KindMap,
				Anon: &ast.MapType{Key: ast.NewIdent("any"), Value: ast.NewIdent("any")}}}
		}
		m := runtime.NewMap(nil, true)
		for k, vv := range x {
			m.Insert(ScriptVal(k), ScriptVal(vv))
		}
		return m
	case runtime.Nil, *runtime.Tuple, *runtime.Cell, *runtime.Slice,
		*runtime.Map, *runtime.Struct, *runtime.Function, *runtime.Closure,
		*runtime.BoundMethod, *runtime.BuiltinFunc, *runtime.GoValue,
		*runtime.Chan, *runtime.TypeDef, *runtime.Iterator, *runtime.Package,
		*runtime.ImportRef, *runtime.Named, *runtime.TypedNil:
		return x
	default:
		// a nil *T of a named host type (os.Open's file on error), or a
		// nil chan/func of a predeclared spelling, is a typed nil, as
		// hostbridge.GoValueOf reads it: equal to nil, typed once stored in an
		// interface. A box would make `f == nil` false.
		if rv := reflect.ValueOf(x); (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Chan || rv.Kind() == reflect.Func || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.IsNil() {
			if tn := hostbridge.HostNil(rv.Type()); tn != nil {
				return tn
			}
		}
		// a fixed-size host array (sha256.Sum256's [32]byte) unboxes to a
		// script slice so `sum[:]` and encoders consume it, matching the
		// handwritten adapters' `sum[:]` result.
		if rv := reflect.ValueOf(x); rv.Kind() == reflect.Array {
			sl := reflect.MakeSlice(reflect.SliceOf(rv.Type().Elem()), rv.Len(), rv.Len())
			reflect.Copy(sl, rv)
			return ScriptVal(sl.Interface())
		}
		return &runtime.GoValue{V: x}
	}
}

// SliceOf unwraps a script slice (cell/pointer derefed, Named-peeled,
// or bare); typed nils and non-slices report not-ok so callers can give
// their own error.
func SliceOf(v runtime.Value) (*runtime.Slice, bool) {
	if d, ok := runtime.Deref(v); ok {
		v = d
	}
	s, ok := runtime.Unwrap(v).(*runtime.Slice)
	return s, ok
}

func StrsSlice(ss []string) runtime.Value {
	if ss == nil {
		// a nil []string result stays nil — SplitN(s, sep, 0) == nil.
		return &runtime.TypedNil{Typ: hostbridge.AnonSliceTyp("string")}
	}
	el := make([]runtime.Value, len(ss))
	for i, s := range ss {
		el[i] = s
	}
	return &runtime.Slice{Elems: el, Typ: hostbridge.AnonSliceTyp("string")}
}

// BytesSliceOf lifts a [][]byte result into []any so ScriptVal turns each
// element into a script []byte slice. A nil result keeps the [][]uint8
// spelling so %T matches Go on a nil [][]byte.
func BytesSliceOf(bb [][]byte) any {
	if bb == nil {
		return &runtime.TypedNil{Typ: hostbridge.AnonSliceTyp("[]uint8")}
	}
	out := make([]any, len(bb))
	for i, b := range bb {
		out[i] = b
	}
	return out
}

// RuneSlice lifts a []rune result into a script slice of rune-tagged
// int64s — the same element representation a []rune{...} literal makes.
func RuneSlice(rs []rune) any {
	if rs == nil {
		return &runtime.TypedNil{Typ: hostbridge.AnonSliceTyp("rune")}
	}
	out := make([]runtime.Value, len(rs))
	for i, r := range rs {
		out[i] = runtime.Tag(runtime.BasicTypedef("rune"), int64(r))
	}
	return &runtime.Slice{Elems: out, Typ: hostbridge.AnonSliceTyp("rune")}
}
