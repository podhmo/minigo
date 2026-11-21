package hostbridge

import (
	"fmt"
	"strings"

	"github.com/podhmo/minigo/runtime"
)

// Display returns a form of v to print with %v, as the print/println
// builtins spell it — nil takes print's spelling ((0x0,0x0) for a nil
// interface, [0/0]0x0 for a nil slice), and it never calls a script
// String or Error method. mc may be nil; a typed constant only
// materializes at its declared width when the caller can materialize.
func Display(mc runtime.ConstMaterializer, v runtime.Value) any {
	switch x := v.(type) {
	case runtime.Nil, *runtime.IfaceNil:
		// print/println spell a nil interface like the runtime's
		// printeface: the (type,value) pair of nil pointers.
		return "(0x0,0x0)"
	case *runtime.TypedNil:
		// print/println spell a nil pointer-ish value like the runtime:
		// 0x0 for pointers/chans/maps/funcs, [0/0]0x0 for a nil slice.
		if x.Typ != nil {
			switch x.Typ.Kind {
			case runtime.KindSlice:
				return "[0/0]0x0"
			case runtime.KindPointer, runtime.KindChan, runtime.KindMap, runtime.KindFunc:
				return "0x0"
			}
		}
		return nil
	case *runtime.UConst:
		nv, err := runtime.UConstNative(x)
		if err != nil {
			return err.Error()
		}
		return Display(mc, nv)
	case *runtime.Named:
		// a typed constant materializes through its declared type, not
		// the default one: `uint64(1<<64 - 1)` is max-uint where the
		// untyped reading would overflow int.
		if mv, ok := ConstAtWidth(mc, x); ok {
			x = runtime.Tag(x.Typ, mv)
		}
		// a declared unsigned-int tag means the int64 payload holds
		// two's-complement bits: `var u uint = 18446744073709551615` and
		// `^uint64(0)` store -1 but print the unsigned reading.
		if iv, ok := x.V.(int64); ok {
			switch runtime.BasicNameOf(x.Typ) {
			case "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte":
				return uint64(iv)
			}
		}
		return Display(mc, x.V)
	case *runtime.Cell:
		return Display(mc, x.Elem)
	case *runtime.Slice:
		parts := make([]any, len(x.Elems))
		for i, e := range x.Elems {
			parts[i] = Display(mc, e)
		}
		return parts
	case *runtime.Tuple:
		// a multi-return value prints like Go's comma-separated values
		parts := make([]string, len(x.Elems))
		for i, e := range x.Elems {
			switch e.(type) {
			case runtime.Nil, *runtime.IfaceNil:
				parts[i] = "<nil>"
			default:
				parts[i] = fmt.Sprint(Display(mc, e))
			}
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case *runtime.Struct:
		parts := make([]any, len(x.Fields))
		for i, e := range x.Fields {
			parts[i] = Display(mc, e)
		}
		return fmt.Sprintf("{%v}", JoinDisplay(parts))
	case *runtime.Map:
		var sb strings.Builder
		sb.WriteString("map[")
		for i := 0; i < x.Len(); i++ {
			k, e := x.At(i)
			if i > 0 {
				sb.WriteByte(' ')
			}
			sb.WriteString(fmt.Sprintf("%v:%v", Display(mc, k), Display(mc, e)))
		}
		sb.WriteByte(']')
		return sb.String()
	case *runtime.GoValue:
		// a host box prints its payload — complex128 as (9+10i),
		// not the wrapper's &{...} pointer spelling.
		return x.V
	default:
		return x
	}
}

// JoinDisplay joins printed parts with a single space, like println's
// separator.
func JoinDisplay(parts []any) string {
	var sb strings.Builder
	for i, p := range parts {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(fmt.Sprint(p))
	}
	return sb.String()
}

// CallOrPanic calls a script callback for its result, letting a script
// panic propagate as the panic it is (callback-in-builtin callers wrap
// when a panic must not cross the host boundary).
func CallOrPanic(v runtime.ScriptCaller, fn runtime.Value, args []runtime.Value) runtime.Value {
	r, err := v.Call(fn, args)
	if err != nil {
		panic(err)
	}
	return r
}

// CallBool runs a script predicate callback for its bool result.
func CallBool(vc runtime.ScriptCaller, fn runtime.Value, args ...runtime.Value) bool {
	b, _ := runtime.Unwrap(CallOrPanic(vc, fn, args)).(bool)
	return b
}
