// scalars.go reads host-boxed values as script scalars: numeric width
// unification (HostFloat/HostScalarInts/HostInt64/HostUint), enum and
// GoValue unwrapping, string operands for concatenation, nil checks and
// map-key eligibility. All are pure reflect helpers with no VM state.
package hostbridge

import (
	"go/constant"
	"reflect"

	"github.com/podhmo/minigo/runtime"
)

// hostFloat reads a value as float64 for float conversions: plain
// int64/float64 pass through, and a boxed host number converts by its
// own kind (uint64 reads unsigned — a wide literal keeps 2^63, not
// the int64 reinterpretation).
func HostFloat(x runtime.Value) (float64, bool) {
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
func HostEnumConv(td *runtime.TypeDef, x runtime.Value) (runtime.Value, bool) {
	if td.HostNew == nil || td.Kind != runtime.KindNamedBasic {
		return nil, false
	}
	hv := td.HostNew()
	if !HostNamedInt(hv) {
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
		return ConstToHostInt(u, rt)
	}
	var iv int64
	var uv uint64
	switch n := x.(type) {
	case int64:
		iv, uv = n, uint64(n)
	case *runtime.GoValue:
		var ok bool
		if iv, uv, ok = HostScalarInts(n); !ok {
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
func HostScalarInts(x runtime.Value) (iv int64, uv uint64, ok bool) {
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
func HostInt64(x runtime.Value) (int64, bool) {
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

// unboxGoValue gives a boxed host value the script value of the same
// shape where one exists, so `[]byte(x)` and `string(x)` see through a
// GoValue carrying []byte, []rune or string — or a named type of those
// kinds (net.IP, template.HTML). Other shapes stay boxed.
func UnboxGoValue(x runtime.Value) runtime.Value {
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
		return &runtime.Slice{Elems: el, Typ: AnonSliceTyp("byte")}
	case []rune:
		el := make([]runtime.Value, len(h))
		for i, r := range h {
			el[i] = NamedBasicElem("rune", int64(r))
		}
		return &runtime.Slice{Elems: el, Typ: AnonSliceTyp("rune")}
	}
	return x
}

// rawHostScalarOperands is twinOperands for a raw host scalar — a bound
// named basic's payload that crossed an interface untagged, as
// any(2*time.Second) does: against a Named of its bound typedef it
// reads as that Named, so the interface comparison sees one type.
func RawHostScalarOperands(a, b runtime.Value) (runtime.Value, runtime.Value) {
	if n, ok := b.(*runtime.Named); ok && n.Typ != nil && n.Typ.HostScalar != nil {
		if _, isNamed := a.(*runtime.Named); !isNamed && reflect.TypeOf(a) == reflect.TypeOf(n.Typ.HostScalar) {
			return runtime.Tag(n.Typ, a), b
		}
	}
	if n, ok := a.(*runtime.Named); ok && n.Typ != nil && n.Typ.HostScalar != nil {
		if _, isNamed := b.(*runtime.Named); !isNamed && reflect.TypeOf(b) == reflect.TypeOf(n.Typ.HostScalar) {
			return a, runtime.Tag(n.Typ, b)
		}
	}
	return a, b
}

// hostStrOperand reads a host named string (template.HTML) — boxed in
// a GoValue, or raw — as its string. Plain strings and the script's own
// scalars are not host strings.
func HostStrOperand(x runtime.Value) (hs HostString, ok bool) {
	switch g := x.(type) {
	case string, int64, float64, bool, nil:
		return hs, false
	case *runtime.GoValue:
		x, hs.Boxed = g.V, true
	}
	rv := reflect.ValueOf(x)
	if rv.Kind() != reflect.String || rv.Type() == reflect.TypeFor[string]() {
		return hs, false
	}
	hs.S, hs.Rt = rv.String(), rv.Type()
	return hs, true
}

// hostNamedNonString is [runtime.HostNamedScalar] minus the string kind:
// host-boxed strings keep comparing by contents (hostStringOperand).
func HostNamedNonString(x runtime.Value) (runtime.Value, reflect.Type, bool) {
	p, rt, ok := runtime.HostNamedScalar(x)
	if !ok || rt.Kind() == reflect.String {
		return nil, nil, false
	}
	return p, rt, true
}

// hostBoxIsNil reports whether a boxed host value holds a nil map,
// slice, pointer, chan or func.
func HostBoxIsNil(gv *runtime.GoValue) bool {
	rv := reflect.ValueOf(gv.V)
	switch rv.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Chan, reflect.Func:
		return rv.IsNil()
	}
	return false
}

// basicKeyPayload reports whether x, a [runtime.Named] key's payload, is a
// basic scalar — a script int64/string/... or a bound host scalar such as
// a raw time.Month — rather than a composite or a host box.
func BasicKeyPayload(x runtime.Value) bool {
	switch x.(type) {
	case int64, string, float64, bool:
		return true
	case nil, *runtime.GoValue, *runtime.UConst:
		return false
	}
	k := reflect.TypeOf(x).Kind()
	return k <= reflect.Complex128 || k == reflect.String
}

// hostNilEmbed reports whether an embedded host-typed field's stored
// value is nil — a TypedNil/IfaceNil slot or a nil pointer box — so a
// member resolved through it panics on the implicit dereference.
func HostNilEmbed(v runtime.Value) bool {
	v = runtime.Unwrap(v)
	if dv, ok := runtime.Deref(v); ok {
		v = dv
	}
	switch x := v.(type) {
	case nil, runtime.Nil, *runtime.TypedNil, *runtime.IfaceNil:
		return true
	case *runtime.GoValue:
		rv := reflect.ValueOf(x.V)
		switch rv.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
			return rv.IsNil()
		}
	}
	return false
}

// hostNilCallable reports whether name is a pointer-receiver method on
// the boxed type — the only member callable through a nil stored
// pointer: Go passes the nil receiver straight to the method, while
// fields and value-receiver methods must dereference it first.
func HostNilCallable(zero any, name string) bool {
	pt := reflect.TypeOf(zero)
	if pt.Kind() != reflect.Pointer {
		return false
	}
	if _, ok := pt.MethodByName(name); !ok {
		return false // a field — reads and writes dereference
	}
	if _, ok := pt.Elem().MethodByName(name); ok {
		return false // value receiver — evaluating it dereferences
	}
	return true
}

// HostString is a host named string operand read as a plain string.
type HostString struct {
	S     string
	Rt    reflect.Type
	Boxed bool // carried in a GoValue (else a raw payload under a Named tag)
}

// constToHostInt converts an integer constant to the host type t, boxed;
// false when the constant is not an integer t can represent.
func ConstToHostInt(u *runtime.UConst, t reflect.Type) (runtime.Value, bool) {
	c := constant.ToInt(u.V)
	if c.Kind() != constant.Int {
		return nil, false
	}
	rv := reflect.New(t).Elem()
	if u.Wrap {
		// a non-constant shift result truncates to t's width: the
		// reflect setters store the low bits.
		w := runtime.WrapInt64(c)
		switch t.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			rv.SetInt(w)
		default:
			rv.SetUint(uint64(w))
		}
		return &runtime.GoValue{V: rv.Interface()}, true
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, exact := constant.Int64Val(c)
		if !exact || rv.OverflowInt(i) {
			return nil, false
		}
		rv.SetInt(i)
	default:
		n, exact := constant.Uint64Val(c)
		if !exact || rv.OverflowUint(n) {
			return nil, false
		}
		rv.SetUint(n)
	}
	return &runtime.GoValue{V: rv.Interface()}, true
}

// unboxGoValue gives a boxed host value the script value of the same
// shape where one exists, so `[]byte(x)` and `string(x)` see through a
// GoValue carrying []byte, []rune or string — or a named type of those
// kinds (net.IP, template.HTML). Other shapes stay boxed.
// namedBasicElem tags a materialized element with its basic type — a
// slice literal's elements coerce through the element typedef (byte
// elements read as uint8s), so unboxed host slices carry the same tag
// or deep equality / %T see a different element type.
func NamedBasicElem(name string, x runtime.Value) runtime.Value {
	return runtime.Tag(runtime.BasicTypedef(name), x)
}

// hostNamedInt reports whether x is a host value of a defined integer
// type — a reflect.Kind or parse.NodeType, not a bare int or int64.
func HostNamedInt(x any) bool {
	t := reflect.TypeOf(x)
	if t == nil || t.PkgPath() == "" {
		return false
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return false
}

// hostStringOperand is what eqlValue compares for a host-boxed string:
// its string contents. Any other box compares as itself.
func HostStringOperand(gv *runtime.GoValue) runtime.Value {
	if rv := reflect.ValueOf(gv.V); rv.IsValid() && rv.Kind() == reflect.String {
		return rv.String()
	}
	return gv
}

// hostNamedInt reports whether x is a host value of a defined integer

// hostStringOperand is what eqlValue compares for a host-boxed string:

// hostString is a host named string operand read as a plain string.
// hostStrOperand reads a host named string (template.HTML) — boxed in
// a GoValue, or raw — as its string. Plain strings and the script's own
// scalars are not host strings.
// uintOperand reads an int-domain runtime value as uint64: int64s carry
// two's-complement bits already and a GoValue holds its boxed unsigned
// width verbatim.
func UintOperand(v runtime.Value) (uint64, bool) {
	switch x := v.(type) {
	case int64:
		return uint64(x), true
	case *runtime.GoValue:
		return HostUint(x.V)
	}
	return 0, false
}
