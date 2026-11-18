// Package vm is the minigo stack-machine interpreter: it runs the
// [bytecode.Chunk] code produced by [github.com/podhmo/minigo/compile].
//
// The host creates a [VM] with its [Hooks] filled in — the engine services
// the VM needs, such as builtins and lazy materialization of package
// members. [VM.Call] invokes a function-like value and is the error
// boundary: script panics and traps come back as errors. A *VM implements
// [runtime.VMCaller], through which host builtins call back into the script.
//
// Each Call runs inside a script process that the `go` statement's
// goroutines share. By default the outermost Call opens the process and
// ends it on return, stopping goroutines still running; to keep one
// process across several Calls, hold it with [VM.EnsureProc] instead.
//
// The process reports a deadlock when all its goroutines are parked with
// nothing left to wake them. Host intrinsics that will call into the
// script later, such as timers, must report the pending callback, or the
// waiting process may be reported as deadlocked.
package vm

import (
	"fmt"
	"go/ast"
	"math"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/podhmo/minigo/runtime"
)

func isFloat(v runtime.Value) bool {
	_, ok := v.(float64)
	return ok
}

func toFloat(v runtime.Value) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case *runtime.Named:
		return toFloat(x.V)
	}
	return 0
}

// Convert implements [runtime.VMCaller], for host intrinsics running on
// this VM's goroutine.
func (v *VM) Convert(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	// Going through Call instead costs a goroutineID (runtime.Stack parse)
	// per conversion — measurable for append on numeric slices.
	return v.convert(td, x)
}

// convert implements T(x) — a call on a *TypeDef.
func (v *VM) convert(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	// an alias IS its target type: `A(3)` with `type A = uint8` is a
	// uint8 (%T, interface identity), and a typed const `const a A`
	// converts the same way.
	if td != nil && td.Kind == runtime.KindAlias {
		if u := v.peelAlias(td); u != nil && u.Kind != runtime.KindAlias {
			td = u
		}
	}
	// nil converts to a typed nil for nilable kinds, NIL for interfaces.
	// Nilability reads through declared chains (`type C B` where B is a
	// slice type takes nil even though C's own kind reads NamedBasic).
	if _, isNil := x.(runtime.Nil); isNil || x == nil {
		utd := td
		k := td.Kind
		if k == runtime.KindNamedBasic || k == runtime.KindAlias {
			if u := v.peelNamed(td); u != nil {
				utd, k = u, u.Kind
			}
		}
		switch k {
		case runtime.KindInterface:
			// nil converts to a nil interface of the declared kind —
			// not bare NIL: `(*ET)(nil) == error(nil)` must be false
			// since a typed nil boxed in an interface is non-nil.
			return &runtime.IfaceNil{Typ: utd}, nil
		case runtime.KindSlice, runtime.KindMap, runtime.KindChan, runtime.KindFunc, runtime.KindPointer:
			return &runtime.TypedNil{Typ: td}, nil
		}
		return nil, fmt.Errorf("cannot convert nil to %s", tdName(td))
	}
	// a typed nil converts to another nilable type by re-tagging when the
	// underlying shapes match (Go requires identical underlying types).
	// Declared chains (`type C B`) peel like the untyped-nil path so the
	// result carries the target's canonical zero form.
	if tn, ok := asTypedNil(x); ok {
		k := td.Kind
		if k == runtime.KindNamedBasic || k == runtime.KindAlias {
			if u := v.peelNamed(td); u != nil {
				k = u.Kind
			}
		}
		switch k {
		case runtime.KindPointer:
			// (*[N]T)(nilSlice) is a slice conversion, not a re-tag —
			// convertPointer decides by the target's array length.
			ut := v.peelNamed(tn.Typ)
			if ut != nil && ut.Kind == runtime.KindSlice {
				break
			}
			// a nil unsafe.Pointer converts to any pointer type, and
			// unsafe.Pointer accepts the nil of any pointer type — both
			// directions skip the identical-underlying rule.
			if v.unsafePtrTd(tn.Typ) || v.unsafePtrTd(td) && ut != nil && ut.Kind == runtime.KindPointer {
				return &runtime.TypedNil{Typ: td}, nil
			}
			if tn.Typ != nil && !v.convShapeEq(tn.Typ, td) {
				return nil, fmt.Errorf("cannot convert %s to %s", tdName(tn.Typ), tdName(td))
			}
			return &runtime.TypedNil{Typ: td}, nil
		case runtime.KindSlice:
			// [N]T(nilSlice) is a slice-to-array conversion producing
			// the zero array — convertArray decides, not the re-tag.
			if ut := v.peelNamed(tn.Typ); ut != nil && ut.Kind == runtime.KindSlice {
				if _, isArr := v.arrayLen(v.topFrame(), td); isArr {
					break
				}
			}
			if tn.Typ != nil && !v.convShapeEq(tn.Typ, td) && !v.hostElemTwin(v.topFrame(), tn.Typ, td) {
				return nil, fmt.Errorf("cannot convert %s to %s", tdName(tn.Typ), tdName(td))
			}
			return &runtime.TypedNil{Typ: td}, nil
		case runtime.KindMap, runtime.KindChan, runtime.KindFunc:
			if tn.Typ != nil && !v.convShapeEq(tn.Typ, td) {
				return nil, fmt.Errorf("cannot convert %s to %s", tdName(tn.Typ), tdName(td))
			}
			return &runtime.TypedNil{Typ: td}, nil
		case runtime.KindInterface:
			return &runtime.IfaceNil{Typ: tn.Typ}, nil
		}
	}
	if r, ok := hostEnumConv(td, x); ok {
		return r, nil
	}
	// an untyped constant converts by Go's representability rules —
	// int64('a'), float64(1e500)'s overflow, string('a'), complex128(3)
	// all land here.
	if u, ok := x.(*runtime.UConst); ok {
		return v.convertConst(td, u)
	}
	// an interface conversion keeps the dynamic pair: the value
	// satisfies the interface's methods AS its dynamic type —
	// `interface{}(F(3))` stays a main.F, never the unwrapped float64.
	if td.Kind == runtime.KindInterface {
		ok, err := v.ifaceSatisfied(td, x)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
		}
		return x, nil
	}
	// a Named value converts through its underlying value — `string(x)` on
	// a named string value works like the underlying conversion; `T(x)`
	// on the same declared type is a no-op. Nested tags peel too — a
	// `type Tsmallv byte` value carries Named{Tsmallv, Named{byte, v}}.
	// A peeled unsigned tag is remembered: float conversions of an
	// unsigned source read the bits as uint64 (float64(uint64(1<<63))
	// is 2^63, not -2^63).
	srcUnsigned := false
	srcUnsafe := false
	peeled := false
	for {
		n, ok := x.(*runtime.Named)
		if !ok {
			break
		}
		peeled = true
		if sameTypeDef(n.Typ, td) {
			return x, nil
		}
		if unsignedName(sizedNameOf(n.Typ)) {
			srcUnsigned = true
		}
		if v.unsafePtrTd(n.Typ) {
			srcUnsafe = true
		}
		x = n.V
	}
	// a nil unsafe.Pointer still counts as an unsafe source for the
	// uintptr mint — uintptr(u) is 0.
	if tn, isTN := asTypedNil(x); isTN && v.unsafePtrTd(tn.Typ) {
		srcUnsafe = true
	}
	// a bound host slice type (net.IP) converts from a host slice, a
	// script slice or a string without unboxing its source first.
	if r, ok, err := v.hostSliceConv(td, x); ok || err != nil {
		return r, err
	}
	// a host value unboxes so its concrete value converts like a script
	// value of the same shape (a []byte arriving boxed becomes a slice).
	x = unboxGoValue(x)
	if peeled {
		// a tagged host named string (template.HTML("x")) holds its raw
		// host payload; it converts as the plain string.
		switch x.(type) {
		case int64, float64, string, bool:
		default:
			if rv := reflect.ValueOf(x); rv.Kind() == reflect.String && rv.Type() != reflect.TypeFor[string]() {
				x = rv.String()
			}
		}
	}
	if u, ok := x.(*runtime.UConst); ok {
		// a peeled typed constant converts in the constant domain like a
		// direct T(c) — `float64(int64(0))` stays a constant.
		return v.convertConst(td, u)
	}
	switch td.Name {
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "byte", "rune", "uintptr":
		var iv int64
		var uv uint64
		switch n := x.(type) {
		case int64:
			iv, uv = n, uint64(n)
		case float64:
			iv, uv = int64(n), uint64(n)
		case string:
			iv = int64([]rune(n)[0]) // int("x") is the first rune's code point
			uv = uint64(iv)
		default:
			// uintptr(unsafePtr) mints the address %p would print —
			// the ref object's own Go address for script refs, the
			// host pointer for a host box. srcUnsafe gates it because
			// gc only allows *T -> uintptr via unsafe.Pointer.
			if srcUnsafe && td.Name == "uintptr" {
				if addr, ok := v.unsafePtrAddr(x); ok {
					iv, uv = int64(addr), addr
					break
				}
			}
			// a host scalar — boxed (a wide uint64 literal, a reflect
			// result) or raw (time.Duration) — converts by its host
			// kind.
			var ok bool
			iv, uv, ok = hostScalarInts(x)
			if !ok {
				return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
			}
		}
		// an unsigned conversion past MaxInt64 keeps its bits boxed —
		// `uint64(1.6717361816799281e+19)` is 16717361816799281152, and a
		// wrapped int64(-9.2e18) would read it as a negative float when
		// converted back ($GOROOT/test/ken/convert.go's tu64 rows).
		// The overflow check only applies to full-width targets — a
		// narrowing conversion masks first: uint16(-3) is 65533, not
		// uint64(-3).
		if unsignedName(td.Name) {
			switch td.Name {
			case "uint8", "byte":
				uv &= 0xFF
			case "uint16":
				uv &= 0xFFFF
			case "uint32":
				uv &= 0xFFFFFFFF
			}
		}
		if unsignedName(td.Name) && uv > math.MaxInt64 {
			return runtime.Tag(td, &runtime.GoValue{V: uv}), nil
		}
		if sizedIntName(td.Name) || td.Name == "int64" {
			// the converted value keeps its declared tag: arithmetic
			// re-wraps to the type's width (-u on uint8 yields 251), %T
			// prints the type name, and unsigned uint64 keeps its
			// domain for %x/%d. `int64(x)` tags too — an explicit
			// conversion declares its type — while bare ints stay
			// untagged (their %T already spells "int").
			return runtime.Tag(td, maskInt(iv, td.Name)), nil
		}
		return maskInt(iv, td.Name), nil
	case "float32":
		if iv, ok := x.(int64); ok && srcUnsigned {
			return runtime.Tag(td, float64(float32(uint64(iv)))), nil
		}
		if f, ok := hostFloat(x); ok {
			return runtime.Tag(td, float64(float32(f))), nil
		}
		switch x.(type) {
		case int64, float64:
			return runtime.Tag(td, float64(float32(toFloat(x)))), nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "float64":
		if iv, ok := x.(int64); ok && srcUnsigned {
			return float64(uint64(iv)), nil
		}
		if f, ok := hostFloat(x); ok {
			return f, nil
		}
		switch x.(type) {
		case int64, float64:
			return toFloat(x), nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "complex64":
		if cv, ok := convComplex(x); ok {
			return &runtime.GoValue{V: complex64(cv)}, nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "complex128":
		if cv, ok := convComplex(x); ok {
			return &runtime.GoValue{V: cv}, nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), td.Name)
	case "string":
		switch sx := x.(type) {
		case string:
			return sx, nil
		case *runtime.TypedNil:
			// string([]byte(nil)) / string([]rune(nil)) yields "" — a
			// nilable source that isn't a slice is a convert error.
			if sx.Typ == nil || sx.Typ.Kind == runtime.KindSlice {
				return "", nil
			}
			return nil, fmt.Errorf("cannot convert %s to string", typeNameOf(x))
		case int64:
			return stringFromInt(sx), nil
		case *runtime.Slice:
			// []byte or []rune -> string: the element family decides.
			// An untyped slice (host-produced) reads as bytes.
			fam := byte('b')
			tn := "[]byte"
			if sx.Typ != nil {
				fam = v.elemFamily(v.elemTypedef(v.topFrame(), sx.Typ))
				tn = tdName(sx.Typ)
			}
			switch fam {
			case 'b':
				bs := make([]byte, 0, len(sx.Elems))
				for _, e := range sx.Elems {
					i, ok := runtime.Unwrap(e).(int64)
					if !ok {
						return nil, fmt.Errorf("cannot convert %s to string", tn)
					}
					bs = append(bs, byte(i))
				}
				return string(bs), nil
			case 'r':
				rs := make([]rune, 0, len(sx.Elems))
				for _, e := range sx.Elems {
					i, ok := runtime.Unwrap(e).(int64)
					if !ok {
						return nil, fmt.Errorf("cannot convert %s to string", tn)
					}
					rs = append(rs, rune(i))
				}
				return string(rs), nil
			}
			return nil, fmt.Errorf("cannot convert %s to string", tdName(sx.Typ))
		case *runtime.GoValue:
			// a host value of string kind — reflect.StructTag from
			// StructField.Tag — converts by its underlying string.
			if rv := reflect.ValueOf(sx.V); rv.Kind() == reflect.String {
				return rv.String(), nil
			}
		default:
			// a raw host scalar of integer kind (time.Duration)
			// converts like int64: string(time.Duration(65)) is "A".
			if sx, ok := hostInt64(x); ok {
				return stringFromInt(sx), nil
			}
			// a raw host scalar of string kind (reflect.StructTag)
			// converts by its underlying string.
			if rv := reflect.ValueOf(x); rv.IsValid() && rv.Kind() == reflect.String {
				return rv.String(), nil
			}
		}
		return nil, fmt.Errorf("cannot convert %s to string", typeNameOf(x))
	case "bool":
		return truthy(x), nil
	}
	// a declared name built on another type — an alias `type A = T` or a
	// chain `type C B` — converts through that type first. An alias yields
	// the target's own identity; `type C B` re-wraps in C's identity.
	switch td.Kind {
	case runtime.KindAlias:
		if u := v.peelAlias(td); u != nil && u != td {
			return v.convert(u, x)
		}
	case runtime.KindNamedBasic:
		if u := v.peelNamed(td); u != nil && u != td {
			cv, err := v.convert(u, x)
			if err != nil {
				return nil, err
			}
			// the inner conversion tags the underlying name — MyI16's
			// int16 — leaving Named{MyI16, Named{int16, v}}; binary ops
			// peel one level and would lose the declared tag. Flatten
			// to the single level the const-materialized form carries.
			if cn, ok := cv.(*runtime.Named); ok && sameTypeDef(cn.Typ, u) {
				cv = cn.V
			}
			return runtime.Tag(td, cv), nil
		}
	}
	switch td.Kind {
	case runtime.KindSlice:
		if an, isArr := v.arrayLen(v.topFrame(), td); isArr {
			return v.convertArray(td, x, an)
		}
		return v.convertSlice(td, x)
	case runtime.KindMap:
		return v.convertMap(td, x)
	case runtime.KindChan:
		return v.convertChan(td, x)
	case runtime.KindPointer:
		return v.convertPointer(td, x, srcUnsafe)
	case runtime.KindStruct:
		return v.convertStruct(td, x)
	case runtime.KindFunc:
		// generalized inference (Go 1.27): `F(Id)` instantiates the
		// generic function against F's signature first — the inferred
		// binds carry over into the (possibly named) result.
		if xv, ierr := v.inferForFuncTarget(x, td); ierr != nil {
			return nil, ierr
		} else {
			x = xv
		}
		switch x.(type) {
		case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
			// a declared func type re-tags so `x.(F)` checks identity
			// and member access sees only F's declared method set —
			// function values carry no swappable tag otherwise.
			if td.Spec != nil {
				return runtime.Tag(td, x), nil
			}
			return x, nil // signatures are not modeled
		}
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	// declared named basic types tag the converted value so its declared
	// identity survives reads (MyInt(5) is MyInt, not int). This point is
	// reached only when the underlying ident did not resolve (no engine
	// hooks or an unbound name) — the declared tag still applies, like
	// every other dynamic fallthrough.
	if td.Kind == runtime.KindNamedBasic {
		if !declaredType(td) {
			return x, nil
		}
		u := x
		if id, ok := td.Anon.(*ast.Ident); ok {
			cv, err := v.convert(&runtime.TypeDef{Kind: runtime.KindNamedBasic, Name: id.Name}, x)
			if err != nil {
				return nil, err
			}
			u = cv
		}
		return runtime.Tag(td, u), nil
	}
	if td.Name != "" {
		return x, nil
	}
	return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
}

// hostFloat reads a value as float64 for float conversions: plain
// int64/float64 pass through, and a boxed host number converts by its
// own kind (uint64 reads unsigned — a wide literal keeps 2^63, not
// the int64 reinterpretation).
func hostFloat(x runtime.Value) (float64, bool) {
	if gv, ok := x.(*runtime.GoValue); ok {
		x = gv.V
	}
	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return 0, false
}

// hostEnumConv converts an integer to a bound host enum (reflect.Kind,
// parse.NodeType) as the boxed host value its constants carry, so
// `parse.NodeType(1) == parse.NodeAction`. A constant must be
// representable; a variable wraps like Go's conversion.
func hostEnumConv(td *runtime.TypeDef, x runtime.Value) (runtime.Value, bool) {
	if td.HostNew == nil || td.Kind != runtime.KindNamedBasic {
		return nil, false
	}
	hv := td.HostNew()
	if !hostNamedInt(hv) {
		return nil, false
	}
	rt := reflect.TypeOf(hv)
	for {
		n, ok := x.(*runtime.Named)
		if !ok {
			break
		}
		x = n.V
	}
	if u, ok := x.(*runtime.UConst); ok {
		return constToHostInt(u, rt)
	}
	var iv int64
	var uv uint64
	switch n := x.(type) {
	case int64:
		iv, uv = n, uint64(n)
	case *runtime.GoValue:
		var ok bool
		if iv, uv, ok = hostScalarInts(n); !ok {
			return nil, false
		}
	default:
		return nil, false
	}
	rv := reflect.New(rt).Elem()
	switch rt.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		rv.SetInt(iv)
	default:
		rv.SetUint(uv)
	}
	return &runtime.GoValue{V: rv.Interface()}, true
}

// hostScalarInts reads a host scalar of integer or float kind into the
// (int64, uint64) pair a numeric conversion needs — a boxed GoValue
// (a wide uint64 literal, a reflect result) and a raw host scalar
// (time.Duration) alike convert by their host kind.
func hostScalarInts(x runtime.Value) (iv int64, uv uint64, ok bool) {
	if gv, is := x.(*runtime.GoValue); is {
		x = gv.V
	}
	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), uint64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int64(rv.Uint()), rv.Uint(), true
	case reflect.Float32, reflect.Float64:
		return int64(rv.Float()), uint64(rv.Float()), true
	}
	return 0, 0, false
}

// hostInt64 reads a host value of integer kind, signed or unsigned —
// the integer source a string(i) conversion accepts (floats do not).
func hostInt64(x runtime.Value) (int64, bool) {
	if gv, ok := x.(*runtime.GoValue); ok {
		x = gv.V
	}
	rv := reflect.ValueOf(x)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return int64(rv.Uint()), true
	}
	return 0, false
}

// stringFromInt converts an integer code point to a one-rune string,
// range-checked like string(int64) — rune(sx) would truncate before
// string() could see the out-of-range value (issue15039).
func stringFromInt(sx int64) string {
	if sx < 0 || sx > utf8.MaxRune {
		return "�"
	}
	return string(rune(sx))
}

// unboxGoValue gives a boxed host value the script value of the same
// shape where one exists, so `[]byte(x)` and `string(x)` see through a
// GoValue carrying []byte, []rune or string — or a named type of those
// kinds (net.IP, template.HTML). Other shapes stay boxed.
func unboxGoValue(x runtime.Value) runtime.Value {
	gv, ok := x.(*runtime.GoValue)
	if !ok {
		return x
	}
	if rv := reflect.ValueOf(gv.V); rv.Kind() == reflect.String && rv.Type() != reflect.TypeFor[string]() {
		return rv.String()
	}
	if s, ok := UnboxHostSlice(gv); ok {
		return s
	}
	switch h := gv.V.(type) {
	case string:
		return h
	case []byte:
		el := make([]runtime.Value, len(h))
		for i, b := range h {
			el[i] = runtime.ByteValue(b)
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("byte")}
	case []rune:
		el := make([]runtime.Value, len(h))
		for i, r := range h {
			el[i] = namedBasicElem("rune", int64(r))
		}
		return &runtime.Slice{Elems: el, Typ: anonSliceTyp("rune")}
	}
	return x
}

// namedBasicElem tags a materialized element with its basic type — a
// slice literal's elements coerce through the element typedef (byte
// elements read as uint8s), so unboxed host slices carry the same tag
// or deep equality / %T see a different element type.
func namedBasicElem(name string, x runtime.Value) runtime.Value {
	return runtime.Tag(runtime.BasicTypedef(name), x)
}

// convertArray implements `[N]T(s)` on array typedefs (Go 1.20): the
// result is a fresh array copying the slice's first N elements — a
// too-short or wrongly-shaped source panics/errors like Go.
func (v *VM) convertArray(td *runtime.TypeDef, x runtime.Value, n int64) (runtime.Value, error) {
	if tn, isNil := asTypedNil(x); isNil {
		if ut := v.peelNamed(tn.Typ); ut != nil && ut.Kind == runtime.KindSlice {
			// a nil slice has length 0 — too short for a non-zero array.
			if n > 0 {
				panic(runtime.SliceToArrayPanic(0, n))
			}
			return &runtime.Slice{Elems: v.zeroElems(v.topFrame(), td, n), Typ: td}, nil
		}
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(tn.Typ), tdName(td))
	}
	if s, ok := runtime.Unwrap(x).(*runtime.Slice); ok {
		// an array source converts only to the identical array type —
		// slicing out a different length is a slice conversion's job.
		if s.Typ != nil {
			if _, srcIsArr := v.arrayLen(v.topFrame(), s.Typ); srcIsArr && !v.tdShapeEq(s.Typ, td) {
				return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
			}
		}
		if sl := s.Len(); sl < n {
			panic(runtime.SliceToArrayPanic(sl, n))
		}
		out := v.zeroElems(v.topFrame(), td, n)
		if s.Virtual() {
			// the elements are all the single zero-size value Zero vends.
			for i := range out {
				out[i] = v.Copy(s.Zero)
			}
		} else {
			copy(out, s.Elems[:n])
		}
		return &runtime.Slice{Elems: out, Typ: td}, nil
	}
	return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
}

// zeroElems fills an array's element slots with the element type's zero.
func (v *VM) zeroElems(f *frame, td *runtime.TypeDef, n int64) []runtime.Value {
	out := make([]runtime.Value, n)
	if et := v.elemTypedef(f, td); et != nil {
		z := v.zeroValue(f, et)
		for i := range out {
			out[i] = z
		}
	}
	return out
}

// convertSlice implements `[]T(x)`: the special string->byte/rune-slice
// conversions plus slice->slice when the underlying shapes are identical
// (element types compare by identity — []int does not convert to
// []MyIntElem, while []uint8 and []byte are the same type).
func (v *VM) convertSlice(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	switch s := x.(type) {
	case string:
		et := v.elemTypedef(v.topFrame(), td)
		switch v.elemFamily(et) {
		case 'b':
			el := make([]runtime.Value, 0, len(s))
			// each distinct byte coerces once: the coerced value is an
			// immutable tagged scalar, so equal bytes can share it.
			var tab [256]runtime.Value
			for _, b := range []byte(s) {
				if tab[b] == nil {
					// elements coerce through the declared element type —
					// []byte("x") carries byte-tagged elements like the
					// []byte{...} literal does.
					tab[b] = v.coerce(v.topFrame(), int64(b), et)
				}
				el = append(el, tab[b])
			}
			return &runtime.Slice{Elems: el, Typ: td}, nil
		case 'r':
			el := make([]runtime.Value, 0, len(s))
			var ascii [utf8.RuneSelf]runtime.Value // shared like the byte table
			for _, r := range s {
				if r >= utf8.RuneSelf {
					el = append(el, v.coerce(v.topFrame(), int64(r), et))
					continue
				}
				if ascii[r] == nil {
					ascii[r] = v.coerce(v.topFrame(), int64(r), et)
				}
				el = append(el, ascii[r])
			}
			return &runtime.Slice{Elems: el, Typ: td}, nil
		}
		return nil, fmt.Errorf("cannot convert string to %s", tdName(td))
	case *runtime.Slice:
		if an, isArr := v.arrayLen(v.topFrame(), td); isArr {
			// [N]T(s) — slice-to-array conversion copies the first N
			// elements (too-short slices panic like Go's runtime check).
			if sl := s.Len(); sl < an {
				panic(runtime.SliceToArrayPanic(sl, an))
			}
			el := s.Elems[:an]
			if s.Virtual() {
				// every element is the single zero-size value Zero vends.
				el = make([]runtime.Value, an)
				for i := range el {
					el[i] = v.Copy(s.Zero)
				}
			}
			return v.copyArray(v.topFrame(), &runtime.Slice{Elems: el, Typ: td}, td), nil
		}
		if s.Typ != nil && !v.convShapeEq(s.Typ, td) && !v.hostElemTwin(v.topFrame(), s.Typ, td) {
			// a host-unboxed slice spells its element bare
			// (`[]*Field`); its kept element identity decides.
			return nil, fmt.Errorf("cannot convert %s to %s", tdName(s.Typ), tdName(td))
		}
		// Go shares the backing array on a conversion: re-tag, no copy.
		// A virtual slice keeps its logical counters.
		return &runtime.Slice{Elems: s.Elems, N: s.N, CapN: s.CapN, Zero: s.Zero, Typ: td}, nil
	}
	return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
}

// convertMap implements `M(x)` on map typedefs: identical underlying
// shapes share the same map (key types are part of the shape). The Named
// wrap keeps the declared tag while sends/indexes resolve through to the
// shared underlying map — Go's conversion aliases the map.
func (v *VM) convertMap(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	m, ok := x.(*runtime.Map)
	if !ok {
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if m.Typ != nil && !v.convShapeEq(m.Typ, td) {
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(m.Typ), tdName(td))
	}
	return runtime.Tag(td, m), nil
}

// convertChan implements `C(x)` on channel typedefs: identical underlying
// shapes share the same channel. The Named wrap keeps the declared tag
// while the underlying queue stays shared — Go's conversion aliases the
// channel.
func (v *VM) convertChan(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	ch, ok := x.(*runtime.Chan)
	if !ok {
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if ch.Typ != nil && !v.convShapeEq(ch.Typ, td) {
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(ch.Typ), tdName(td))
	}
	return runtime.Tag(td, ch), nil
}

// convertPointer implements `*T(x)` and `P(x)` on pointer typedefs. A
// pointer in this model is a cell — it carries no swappable type tag, so
// the conversion checks the pointee's declared shape when one is known
// and passes the pointer itself through.
func (v *VM) convertPointer(td *runtime.TypeDef, x runtime.Value, srcUnsafe bool) (runtime.Value, error) {
	if tn, isNil := asTypedNil(x); isNil {
		// (*[N]T)(nilSlice) — a nil slice converts to a nil array
		// pointer only when N == 0; a larger array conversion panics
		// on the missing backing store like Go.
		if ut := v.peelNamed(tn.Typ); ut != nil && ut.Kind == runtime.KindSlice {
			if et := v.elemTypedef(v.topFrame(), td); et != nil {
				if an, isArr := v.arrayLen(v.topFrame(), et); isArr {
					if an == 0 {
						return &runtime.TypedNil{Typ: td}, nil
					}
					panic(runtime.SliceToArrayPanic(0, an))
				}
			}
		}
	}
	if s, isSlice := runtime.Unwrap(x).(*runtime.Slice); isSlice {
		// (*[N]T)(s) — slice-to-array-pointer conversion shares the
		// slice's backing array (too-short slices panic like Go's).
		if et := v.elemTypedef(v.topFrame(), td); et != nil {
			if an, isArr := v.arrayLen(v.topFrame(), et); isArr {
				if sl := s.Len(); sl < an {
					panic(runtime.SliceToArrayPanic(sl, an))
				}
				el := s.Elems[:an]
				if s.Virtual() {
					// zero-size elements share one address in Go too — a
					// materialized view of the single value reads the same.
					el = make([]runtime.Value, an)
					for i := range el {
						el[i] = v.Copy(s.Zero)
					}
				}
				return &runtime.Cell{Elem: &runtime.Slice{Elems: el, Typ: et}}, nil
			}
		}
	}
	if v.unsafePtrTd(td) {
		// unsafe.Pointer(x) converts from any pointer: a script ref
		// dereferences, a host pointer arrives as a GoValue box, and an
		// engine-internal handle IS a pointer. The Named tag carries the
		// unsafe.Pointer identity while the payload keeps write-through.
		// uintptr -> unsafe.Pointer cannot mint a ref from an integer
		// and stays a convert error.
		if !v.unsafePtrLike(x) {
			return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
		}
		return runtime.Tag(td, x), nil
	}
	if !srcUnsafe && v.hostPtrConv(td, x) {
		// a host pointer (time.UTC) converts to a pointer type whose
		// pointee is that host type — `type timeLocation
		// *time.Location` in log/slog, or back to *time.Location.
		// The box carries no pointee tag to check or re-tag, so only
		// a declared target needs the Named wrap.
		if td.Spec != nil {
			return runtime.Tag(td, x), nil
		}
		return x, nil
	}
	if _, ok := runtime.Deref(x); !ok && !(srcUnsafe && hostPtrBox(x)) {
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	var ptag *runtime.TypeDef
	// an unsafe.Pointer source converts to any *T — Go exempts it from
	// the identical-pointee rule, so the gate below is skipped.
	if pt := v.pointeeTag(x); pt != nil && v.H.ElemOf != nil && !srcUnsafe {
		ptag = pt
		if et, err := v.H.ElemOf(v.peelNamed(td)); err == nil && et != nil && !sameTypeDef(pt, et) && !sameTypeDef(pt, v.peelAlias(et)) {
			pe, pp := v.peelNamed(et), v.peelNamed(pt)
			if pe == nil || pp == nil || pe.Kind != pp.Kind ||
				(pe.Kind == runtime.KindStruct && !structFieldsEq(pe, pp)) ||
				(pe.Kind != runtime.KindStruct && !v.convShapeEq(pe, pp)) {
				return nil, fmt.Errorf("cannot convert *%s to %s", tdName(pt), tdName(td))
			}
		}
	}
	// a declared pointer type re-tags so `x.(P)` checks identity and
	// member access sees only P's declared method set — a bare cell's
	// dynamic type stays the anonymous *Elem. An anonymous *Declared
	// (`(*T)(p)` on a declared T) wraps too: the pointer type is unnamed,
	// but the value must keep T's declared identity so interface checks
	// and method dispatch see the pointee's method set. So does an
	// anonymous pointer whose pointee type the conversion actually
	// changes (`(*uint)(&w)` on `var w uval`): without the tag the
	// result still reads as *uval and the assign site rejects it.
	if td.Spec != nil {
		return runtime.Tag(td, x), nil
	}
	if v.H.ElemOf != nil {
		if et, err := v.H.ElemOf(td); err == nil && et != nil &&
			(declaredType(et) || (ptag != nil && !sameTypeDef(ptag, et) && !sameTypeDef(ptag, v.peelAlias(et)))) {
			return runtime.Tag(td, x), nil
		}
	}
	return x, nil
}

// hostPtrConv reports whether x is a host pointer box whose type is
// exactly the pointer type td spells: td's element is a bound host
// type, so `P(x)` is Go's identical-underlying-type conversion.
func (v *VM) hostPtrConv(td *runtime.TypeDef, x runtime.Value) bool {
	if !hostPtrBox(x) || v.H.ElemOf == nil {
		return false
	}
	et, err := v.H.ElemOf(v.peelNamed(td))
	if err != nil || et == nil {
		return false
	}
	if a := v.peelAlias(et); a != nil {
		et = a
	}
	if et.HostNew == nil {
		return false
	}
	rt := reflect.TypeOf(et.HostNew())
	if rt == nil {
		return false
	}
	if rt.Kind() != reflect.Pointer {
		rt = reflect.PointerTo(rt)
	}
	return rt == reflect.TypeOf(x.(*runtime.GoValue).V)
}

// convertStruct implements `S(x)` on struct typedefs: legal when the
// source is a struct whose field list matches the target's (the
// approximation of Go's identical-underlying rule — field types and tags
// are not modeled). The result is a copy carrying the target's typedef.
func (v *VM) convertStruct(td *runtime.TypeDef, x runtime.Value) (runtime.Value, error) {
	s, ok := x.(*runtime.Struct)
	if !ok {
		if dv, isRef := runtime.Deref(x); isRef {
			s, ok = dv.(*runtime.Struct)
		}
	}
	if !ok {
		return nil, fmt.Errorf("cannot convert %s to %s", typeNameOf(x), tdName(td))
	}
	if s.Def != nil && s.Def != td && !structFieldsEq(s.Def, td) {
		return nil, fmt.Errorf("cannot convert %s to %s", tdName(s.Def), tdName(td))
	}
	return &runtime.Struct{Def: td, Fields: append([]runtime.Value{}, s.Fields...)}, nil
}

// convShapeEq reports whether two typedefs have identical underlying
// shape for a conversion: both peel through declared chains to their
// structural spelling — `type C B` gives B's shape, `Wrap[int]` gives
// []int — while element positions compare by written identity, so []MyInt
// does not spell []int.
func (v *VM) convShapeEq(a, b *runtime.TypeDef) bool {
	sa, sb := v.underlyingShape(a), v.underlyingShape(b)
	return sa != "" && sa == sb
}

// underlyingShape spells a typedef's underlying shape. Declared chain
// heads (type C B, aliases) peel away; composite shapes spell their type
// expression with generic binds substituted.
func (v *VM) underlyingShape(td *runtime.TypeDef) string {
	u := v.peelNamed(td)
	if u == nil {
		return ""
	}
	if s := runtime.TypUnderlyingSpelling(u); s != "" {
		return s
	}
	// a typedef synthesized at a boundary (`&x` yields
	// {Kind: KindPointer, Elem: pointee}) carries its shape as Kind+Elem
	// with no Anon/Spec/Name to spell — rebuild it from those.
	return v.synthShape(u)
}

// synthShape spells a typedef that has no AST or name: only Kind and the
// resolved Elem chain. Element positions keep the declared identity, the
// same way TypSpelling renders a bound type argument (`*N`, not `*int`).
func (v *VM) synthShape(td *runtime.TypeDef) string {
	if td == nil {
		return ""
	}
	if td.Name != "" {
		if td.Pkg != nil {
			if strings.HasPrefix(td.Name, td.Pkg.Path+".") {
				return td.Name
			}
			return td.Pkg.Path + "." + td.Name
		}
		return runtime.CanonicalBasicName(td.Name)
	}
	// an anonymous element still spells its own AST — *struct{a int}
	// and *struct{b string} must not collapse to the same "*".
	src := td.TypeExpr()
	if src != nil {
		return runtime.TypSpelling(src, td)
	}
	var elem string
	if td.Elem != nil {
		elem = v.synthShape(td.Elem)
	}
	switch td.Kind {
	case runtime.KindPointer:
		return "*" + elem
	case runtime.KindSlice:
		return "[]" + elem
	case runtime.KindChan:
		return "chan " + elem
	}
	return ""
}

// elemFamily classifies a slice element typedef for the special string
// conversions: 'b' when its underlying is byte-family (byte, uint8, or a
// named type on them like `type MyByte byte`), 'r' for rune-family, else
// 0 — mirroring Go's `[]byte(s)` / `[]rune(s)` rule.
func (v *VM) elemFamily(et *runtime.TypeDef) byte {
	u := v.peelNamed(et)
	if u == nil {
		return 0
	}
	name := u.Name
	if name == "" {
		if id, ok := u.Anon.(*ast.Ident); ok {
			name = id.Name
		}
	}
	switch name {
	case "byte", "uint8":
		return 'b'
	case "rune", "int32":
		return 'r'
	}
	return 0
}

// coerce applies a declared type `td` to the value being bound: NIL picks
// up the type's zero value (var x T), a TypedNil crossing into an
// interface-typed slot boxes as an IfaceNil (var x any = (*int)(nil)),
// and `var x T = v` runs an assignability check — a value of a different
// named type or a mismatched basic family traps, like Go's type checker.
// Declared named basic types tag the result as *runtime.Named so the
// declared identity survives reads.
func (v *VM) coerce(f *frame, x runtime.Value, td *runtime.TypeDef) runtime.Value {
	// a slot store copies like a Go assignment: `key{mark: p.mark}` must
	// not alias p.mark through the literal's field slot.
	x = valueCopy(x)
	if td == nil {
		return x
	}
	// a HostNew slot stores a pointer-shaped host box whose DECLARED
	// type is the struct itself — assignment copies the struct like gc:
	// `var b bytes.Buffer = a` must not alias a's box. Pointer-typed
	// targets keep sharing (`var p *bytes.Buffer` binds a cell).
	// A value already carrying a HostNew struct tag was just snapshotted
	// by valueCopy (runtime.Copy's Named branch) — cloning again would
	// only snapshot the snapshot; the call still matters for a box with
	// no tag of its own (a host function's return bound to a declared
	// slot).
	if td.Kind == runtime.KindStruct && td.HostNew != nil && !hostNewStructTagged(x) {
		x = hostStructCopy(x)
	}
	if td.Kind == runtime.KindAlias {
		// aliases peel one hop at a time so each intermediate named type
		// keeps its own coerce (a Named value must land on its declared
		// type — `type A = Str` binds a Str-tagged value).
		if u := v.peelAlias(td); u != td {
			return v.coerce(f, x, u)
		}
	}
	if _, isNil := x.(runtime.Nil); isNil {
		return v.zeroValue(f, td)
	}
	if td.Kind == runtime.KindInterface {
		// an untyped constant binds an interface at its default type —
		// `var a any = 'a'` holds a rune, not the lazy constant.
		x = v.materialize(f, x)
		if tn, ok := x.(*runtime.TypedNil); ok {
			// boxing a typed nil still checks the method set: (*int)(nil)
			// cannot bind an interface that requires methods.
			if !v.satisfiesIface(f, td, x) {
				f.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
			}
			return &runtime.IfaceNil{Typ: tn.Typ}
		}
		if !v.satisfiesIface(f, td, x) {
			f.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
		}
		return x
	}
	// generalized inference (Go 1.27): a generic function bound to a
	// func-typed slot — assignment, composite element, call argument or
	// channel send — infers its unbound type parameters from the target
	// signature. `var h func(int) int = Id` instantiates Id[int].
	xv, err := v.inferForFuncTarget(x, td)
	if err != nil {
		f.trap("%s", err)
	}
	return v.coerceConcrete(f, xv, td)
}

// coerceConcrete applies td to a non-nil x under a non-interface target.
// A Named value keeps its identity only for the identical declared type
// (Go: named-to-named needs a conversion); GoValues pass unchecked at the
// host boundary; a TypedNil re-tags when the underlying shape matches.
// convComplex reads a value into the complex domain for a conversion:
// ints and floats promote to complex128, boxed complex values pass.
func convComplex(x runtime.Value) (complex128, bool) {
	switch n := x.(type) {
	case int64:
		return complex(float64(n), 0), true
	case float64:
		return complex(n, 0), true
	case *runtime.GoValue:
		if cv, _, ok := asComplex(n); ok {
			return cv, true
		}
	}
	return 0, false
}

func (v *VM) coerceConcrete(f *frame, x runtime.Value, td *runtime.TypeDef) runtime.Value {
	if u, ok := x.(*runtime.UConst); ok && td.HostNew != nil && td.Kind == runtime.KindNamedBasic {
		// a bound host enum (reflect.Kind, parse.NodeType) holds the
		// boxed host value its constants and host results carry, so
		// `var k reflect.Kind = 2` compares equal to reflect.Int.
		if hv := td.HostNew(); hostNamedInt(hv) {
			if r, ok := constToHostInt(u, reflect.TypeOf(hv)); ok {
				return r
			}
		}
	}
	if u, ok := x.(*runtime.UConst); ok {
		if utd := v.peelNamed(td); utd != nil && basicNameOf(utd) != "" {
			// a constant converts straight to the declared basic type:
			// `var r MyRune = 'a'`, `var i int8 = 300`'s overflow trap.
			return v.materializeConst(f, u, td)
		}
		// other targets take the default type, then assign normally.
		x = v.materialize(f, x)
	}
	if n, ok := x.(*runtime.Named); ok {
		// a Named value keeps its identity only for the identical declared
		// type — aliases count (they ARE the type), `type A B` chains do
		// not (Go: named-to-named needs a conversion).
		if sameTypeDef(n.Typ, td) || sameTypeDef(n.Typ, v.peelAlias(td)) {
			if u, ok := n.V.(*runtime.UConst); ok {
				// a typed constant materializes at the storage
				// boundary — `a[i] = uint8(c)` stores a concrete
				// uint8 so value readers (string([]byte), copy,
				// map keys) see plain values.
				return v.materializeConst(f, u, td)
			}
			return x
		}
		// identical underlying types assign when at least one side is
		// unnamed — `type Number *Number`'s `*x` (Number) binds a
		// *Number parameter, and `map[string]int(m)`'s anonymous
		// conversion result rebinds a named slot, taking its declared
		// tag. Named-to-named still needs a conversion.
		if v.tdShapeEq(n.Typ, td) {
			if !tagIsNamed(td) {
				return x
			}
			if !tagIsNamed(n.Typ) {
				return runtime.Tag(td, n.V)
			}
		}
		// a host-produced slice tagged with the target type's twin —
		// a host []fs.DirEntry feeding a source-interpreted io/fs's
		// `[]DirEntry` slot — re-tags to the declared type when the
		// spellings agree: its elements are already script values
		// (each a GoValue box dispatching by reflection). The tag must
		// be host-minted and the target a slice type: a script-declared
		// same-named type is a different type, and stamping a
		// slice-named payload with a struct target is nonsense.
		if s, ok := runtime.Unwrap(n.V).(*runtime.Slice); ok &&
			v.sameTypedef(n.Typ, td) && v.peelNamed(td).Kind == runtime.KindSlice {
			return stampContainerTyp(s, td)
		}
		f.trap("cannot use %s as %s", tdName(n.Typ), tdName(td))
	}
	if gv, ok := x.(*runtime.GoValue); ok {
		// a complex value coerces into a complex-typed slot at the slot's
		// width; other host boxes pass through unchecked as before.
		switch gv.V.(type) {
		case complex64, complex128:
			if n := basicNameOf(v.peelNamed(td)); n == "complex64" || n == "complex128" {
				cv, _, _ := asComplex(gv)
				if n == "complex64" {
					x = &runtime.GoValue{V: complex64(cv)}
				} else {
					x = &runtime.GoValue{V: cv}
				}
				if td != nil && declaredType(td) {
					return runtime.Tag(td, x)
				}
			}
		case uint64:
			// a wide uint64 box into an integer slot takes the declared
			// tag like a fitting constant would — `var u uint =
			// 18446744073709551615` is a uint, not a bare uint64.
			if n := basicNameOf(v.peelNamed(td)); sizedIntName(n) {
				return runtime.Tag(td, gv)
			}
		}
		// a host numeric converts to a float/complex slot by value —
		// `var f float64 = 16717361816799281152` keeps its unsigned
		// magnitude instead of binding the GoValue raw. The narrowed
		// float64 flows on through the same rounding and tagging a
		// script float takes.
		if fv, ok := hostFloat(gv); ok {
			switch basicNameOf(v.peelNamed(td)) {
			case "float32", "float64", "complex64", "complex128":
				x = fv
			default:
				return x
			}
		} else {
			return x // host boundary: assignability is unknowable
		}
	}
	if tn, ok := x.(*runtime.TypedNil); ok {
		utd0 := v.peelNamed(td)
		if sameTypeDef(tn.Typ, td) || v.tdShapeEq(tn.Typ, td) || v.samePointeeAlias(tn.Typ, td) ||
			// A nil carries no payload, so a value tagged with the host
			// twin of the target type — a source-interpreted io/fs's
			// `[]DirEntry` result fed by a host []fs.DirEntry nil —
			// re-tags to the declared type when the spellings agree.
			// Restricted to host-minted twin tags of container targets:
			// a script-declared same-named type stays distinct.
			(v.sameTypedef(tn.Typ, td) && utd0 != nil &&
				(utd0.Kind == runtime.KindSlice || utd0.Kind == runtime.KindMap || utd0.Kind == runtime.KindChan)) ||
			// a host nil []*ast.Ident into go/ast's `[]N` with N=*Ident.
			v.hostElemTwin(f, tn.Typ, td) ||
			// a host nil *ast.FieldList into go/ast's *FieldList.
			v.hostTwinIs(f, tn.Typ, td) {
			return &runtime.TypedNil{Typ: td} // re-tag to the declared type
		}
		if HostNilPtrType(tn.Typ) != nil {
			// a nil host *T whose twin the slot's type cannot be matched
			// to (a renamed import of a bound package that registers no
			// entry for T): bind it as the untyped nil it read as before
			// — the slot's zero — rather than trap on a program gc
			// accepted.
			return v.zeroValue(f, td)
		}
		f.trap("cannot use nil %s as %s", tdName(tn.Typ), tdName(td))
	}
	if bn := runtime.BoxedNilTyp(x); bn != nil {
		// an interface holding a typed nil unboxes back to that typed
		// nil on a matching concrete slot — reflect.Call marshals a
		// Zero(*T).Interface() arg into the callee's *T parameter.
		utd0 := v.peelNamed(td)
		if sameTypeDef(bn, td) || v.tdShapeEq(bn, td) || v.samePointeeAlias(bn, td) ||
			(v.sameTypedef(bn, td) && utd0 != nil &&
				(utd0.Kind == runtime.KindSlice || utd0.Kind == runtime.KindMap || utd0.Kind == runtime.KindChan)) ||
			v.hostTwinIs(f, bn, td) {
			return &runtime.TypedNil{Typ: td}
		}
	}
	if _, ok := x.(*runtime.IfaceNil); ok {
		f.trap("cannot use interface value as %s", tdName(td))
	}
	// values carrying a declared tag (a named struct's Def, a stamped
	// container Typ) follow the named-to-named rule like *Named —
	// `var a A = sq` traps even though A shares Sq's storage. The trap
	// fires only when the target is named too: an unnamed target
	// (`var m map[string]int = om`) stays a shape check, and anonymous
	// values carry no tag at all.
	utd := v.peelNamed(td)
	if tag := declaredTag(x); tag != nil {
		if sameTypeDef(tag, td) || sameTypeDef(tag, v.peelAlias(td)) {
			return x
		}
		// a host-produced slice tagged with the target type's twin —
		// a host []fs.DirEntry feeding a source-interpreted io/fs's
		// `[]DirEntry` slot — re-tags to the declared type when the
		// spellings agree: its elements are already script values
		// (each a GoValue box dispatching by reflection).
		if s, ok := runtime.Unwrap(x).(*runtime.Slice); ok &&
			utd != nil && utd.Kind == runtime.KindSlice && v.sameTypedef(tag, td) {
			return stampContainerTyp(s, td)
		}
		// a named target whose underlying is an interface (`type Token
		// any`) assigns by interface satisfaction below, not by tag —
		// the tag mismatch is only fatal for concrete targets.
		if tagIsNamed(td) && (utd == nil || utd.Kind != runtime.KindInterface) {
			f.trap("cannot use %s as %s", tdName(tag), tdName(td))
		}
	}
	if utd.Kind == runtime.KindPointer && v.H.ElemOf != nil {
		// `var p P = &v` — the pointee's declared type must match the
		// pointer's element type (a named pointer binds only its own
		// pointee type); untyped pointees defer to the shape check.
		if ptag := v.pointeeTag(x); ptag != nil {
			// an alias pointee (`type cache = [256]string`, new(cache))
			// IS its target: peel it, and an unnamed target compares by
			// shape like any other anonymous type.
			pt := v.peelAlias(ptag)
			// array lengths fold to their constant value before identity
			// compares — `[maxNumLit + maxNumDist]int` and `[316]int`
			// are one type (arrayLen is a no-op on non-array typedefs).
			v.arrayLen(f, pt)
			if et, err := v.H.ElemOf(utd); err == nil && et != nil {
				v.arrayLen(f, et)
				if !sameTypeDef(ptag, et) && !sameTypeDef(ptag, v.peelAlias(et)) &&
					!sameTypeDef(pt, v.peelAlias(et)) &&
					(tagIsNamed(pt) || !v.tdShapeEq(pt, et)) {
					f.trap("cannot use %s as %s", "&"+tdName(ptag), tdName(td))
				}
			}
		} else if et, err := v.H.ElemOf(utd); err == nil && et != nil &&
			et.HostNew != nil && x != nil && reflect.TypeOf(x) == et.HostType() {
			// a bound typedef's pointer slot takes the raw native value
			// the intrinsics return — *runtime.Package rides no Cell or
			// GoValue tag for pointeeTag to read, so identity compares on
			// the Go type like the typeAssert path does.
			return x
		}
	}
	if utd.Kind == runtime.KindInterface {
		// `type I2 I` — the declared name's method set is the underlying
		// interface's; check and store unboxed, like a direct I slot.
		if !v.satisfiesIface(f, utd, x) {
			f.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
		}
		return x
	}
	if !v.shapeOK(f, x, utd) {
		// a raw host value of a bound named basic carries no tag the
		// declared-tag check could see — arithmetic on time.Second
		// yields a bare time.Duration, so a declared target of the same
		// name binds it by name (identical types assign in Go).
		if td.Name != "" && typeNameOf(x) == td.Name {
			return runtime.Tag(td, x)
		}
		println("T0:", typeNameOf(x), "|", tdName(td))
		f.trap("cannot use %s as %s", typeNameOf(x), tdName(td))
	}
	// array-typed slots copy on assignment: `var b = a` owns its own
	// backing array, like Go's value semantics for arrays.
	if _, isArr := v.arrayLen(f, utd); isArr {
		if s, ok := x.(*runtime.Slice); ok {
			x = v.copyArray(f, s, utd)
		}
	}
	// an untyped int constant lands already converted: a float slot
	// takes float64(3) so `var f float64 = 3; f / 2` is 1.5, and a
	// sized-int slot wraps like a conversion (`var x int8 = y` keeps
	// the low byte of a non-constant operand). A bare int64 from an
	// int-typed variable converts too — the VM cannot distinguish the
	// two, and a Named int64 traps in the declared-tag check above.
	if iv, ok := runtime.Unwrap(x).(int64); ok && !declaredType(utd) {
		switch n := tdName(utd); n {
		case "float32":
			x = float64(float32(iv))
		case "float64":
			x = float64(iv)
		case "complex64":
			x = &runtime.GoValue{V: complex64(complex(float64(iv), 0))}
		case "complex128":
			x = &runtime.GoValue{V: complex(float64(iv), 0)}
		default:
			x = maskInt(iv, n)
		}
	}
	// assigning into a float32 slot narrows the way T(x) does —
	// `var f float32 = 0.1` stores the float64 that reads back as
	// float32(0.1), not the source literal's extra bits.
	if fv, ok := runtime.Unwrap(x).(float64); ok {
		switch basicNameOf(utd) {
		case "float32":
			x = float64(float32(fv))
		case "complex64":
			x = &runtime.GoValue{V: complex64(complex(fv, 0))}
		case "complex128":
			x = &runtime.GoValue{V: complex(fv, 0)}
		}
	}
	switch utd.Kind {
	case runtime.KindStruct:
		// an anonymous struct literal assigned to a named struct type
		// takes the declared tag: `var s S = struct{x int}{...}` binds
		// S's method set, not the literal's anonymous typedef. The
		// struct keeps its own Def (fields live there); the Named wrap
		// carries the declared identity.
		if _, ok := x.(*runtime.Struct); ok && tagIsNamed(td) {
			x = runtime.Tag(td, x)
		}
	case runtime.KindMap, runtime.KindSlice, runtime.KindChan:
		if ct := containerTyp(x); ct == nil {
			// a declared container type stamps the value so element
			// reads/writes coerce and missing-key reads yield the
			// declared element zero instead of NIL.
			x = stampContainerTyp(x, td)
		} else if !sameTypeDef(ct, td) {
			if v.sameTypedef(ct, td) || v.hostElemTwin(f, ct, td) {
				// a host-produced container tagged with the target
				// type's twin — a host []fs.DirEntry feeding a
				// source-interpreted io/fs's `[]DirEntry` slot, or a
				// host []ast.Stmt feeding go/ast's `[]N` with N=Stmt —
				// re-tags to the declared type: its elements are
				// already script values (GoValue boxes dispatching
				// by reflection).
				x = stampContainerTyp(x, td)
			} else {
				// two named container types do not re-bind (Go:
				// named-to-named needs a conversion); anonymous/
				// underlying shapes may re-bind only when the shapes
				// match element-for-element.
				if ct.Name != "" && td.Name != "" {
					f.trap("cannot use %s as %s", tdName(ct), tdName(td))
				}
				if !v.tdShapeEval(f, ct, td) {
					f.trap("cannot use %s as %s", tdName(ct), tdName(td))
				}
				// an assignable value takes the slot's declared type: `var n
				// nat = []Word{...}` reads back as nat (its method set), not
				// as the source value's anonymous []Word tag.
				x = stampContainerTyp(x, td)
			}
		}
	}
	switch td.Kind {
	case runtime.KindNamedBasic:
		// declared types and sized-int builtins tag the bound value —
		// without the tag, arithmetic on `var x uint8` loses its width
		// (maskInt at bind time wraps the constant, but -x has nothing
		// to re-wrap against). int64 and float32 tag too, so %T spells
		// the declared width; int and float64 stay bare since the bare
		// value already spells them.
		if declaredType(td) || sizedIntName(td.Name) ||
			td.Name == "int64" || td.Name == "float32" {
			return runtime.Tag(td, x)
		}
	case runtime.KindPointer, runtime.KindFunc:
		// declared pointer/func types tag the bound value so asserts
		// check declared identity and member access sees only the
		// declared method set. Anonymous *T/func() binds stay bare —
		// they carry the pointee's members (T's method set promotes).
		if td.Spec != nil {
			return runtime.Tag(td, x)
		}
	}
	return x
}
