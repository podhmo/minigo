package stdlib

import (
	"math"
	"reflect"
	"time"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// NumNaNOf reports whether v's scalar payload is a NaN float —
// slices.Max/Min propagate NaN instead of dropping it.
func NumNaNOf(v runtime.Value) bool {
	n, ok := scalarNum(v)
	return ok && n.cls == 2 && math.IsNaN(n.f)
}

// PosZeroOf reports whether v's scalar payload is +0.
func PosZeroOf(v runtime.Value) bool {
	n, ok := scalarNum(v)
	return ok && n.cls == 2 && n.f == 0 && !math.Signbit(n.f)
}

// NegZeroOf reports whether v's scalar payload is -0 — gc's
// slices.Min prefers it and slices.Max prefers +0.
func NegZeroOf(v runtime.Value) bool {
	n, ok := scalarNum(v)
	return ok && n.cls == 2 && n.f == 0 && math.Signbit(n.f)
}

// RetErr wraps a (n int, err error) or single-value+error result into
// the script-visible tuple (value, err-or-nil).
func RetErr(n int, err error) (any, error) {
	return &runtime.Tuple{Elems: []runtime.Value{int64(n), ErrVal(err)}}, nil
}

// RetErr2 wraps a (v T, err error) result into the (value, err-or-nil)
// tuple with the value lifted to its script representation.
func RetErr2[T any](v T, err error) (any, error) {
	return &runtime.Tuple{Elems: []runtime.Value{ScriptVal(v), ErrVal(err)}}, nil
}

// IntOf reads a script value as a host int: Named peels, a UConst
// materializes at its default type (the same fold a builtin argument
// position performs), defined numerics read through reflect.
func IntOf(v any) int {
	switch x := v.(type) {
	case *runtime.Named:
		return IntOf(x.V)
	case *runtime.UConst:
		// a named constant stays a UConst through declaration storage;
		// a builtin argument position materializes it at its default
		// type — Go folds these calls at compile time.
		return IntOf(hostbridge.MustNativeConst(x))
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
	case time.Duration:
		return int(x)
	case *runtime.GoValue:
		return IntOf(x.V)
	}
	// a defined numeric type the switch above missed (e.g. fs.FileMode
	// from a host call's result) still carries its integer value — Go
	// would fold passing it to an int-typed parameter at compile time.
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int(rv.Uint())
	}
	return 0
}

// Int64Of is IntOf widened to int64.
// DurOf materializes a numeric script value as time.Duration.
func DurOf(v any) time.Duration { return time.Duration(Int64Of(v)) }

func Int64Of(v any) int64 { return int64(IntOf(v)) }

// FloatOf reads a script value as a host float64.
func FloatOf(v any) float64 {
	switch x := v.(type) {
	case *runtime.Named:
		return FloatOf(x.V)
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

// ByteSlice unmarshals a script value to []byte for os.WriteFile & co:
// accepts strings, []byte natives and int64 element slices.
func ByteSlice(v any) []byte {
	if n, ok := v.(*runtime.Named); ok {
		return ByteSlice(n.V)
	}
	switch x := v.(type) {
	case string:
		return []byte(x)
	case []byte:
		return x
	case []any:
		out := make([]byte, len(x))
		for i, e := range x {
			out[i] = byte(Int64Of(e))
		}
		return out
	case *runtime.Slice:
		out := make([]byte, len(x.Elems))
		for i, e := range x.Elems {
			// byte elements are Named{byte, int64} or raw int64 — read
			// them directly instead of a hostbridge.GoNative walk per element.
			if n, ok := e.(*runtime.Named); ok && (n.Typ == nil || n.Typ.HostScalar == nil) {
				e = n.V
			}
			if iv, ok := e.(int64); ok {
				out[i] = byte(iv)
				continue
			}
			out[i] = byte(Int64Of(hostbridge.GoNative(e)))
		}
		return out
	}
	// a named byte slice — json.RawMessage (jsontext.Value), a host
	// []byte typedef — misses the []byte case above because a type
	// switch needs the exact dynamic type.
	if rv := reflect.ValueOf(v); rv.IsValid() && rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
		return rv.Bytes()
	}
	return nil
}
