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
	"go/constant"
	"go/token"
	"math"
	"reflect"
	"unicode/utf8"

	"github.com/podhmo/minigo/bytecode"
	"github.com/podhmo/minigo/runtime"
)

// sameBareScalar reports whether x and y are bare Go scalars of the
// same kind (int64, float64, string or bool).
func sameBareScalar(x, y runtime.Value) bool {
	switch x.(type) {
	case int64:
		_, ok := y.(int64)
		return ok
	case float64:
		_, ok := y.(float64)
		return ok
	case string:
		_, ok := y.(string)
		return ok
	case bool:
		_, ok := y.(bool)
		return ok
	}
	return false
}

// bareScalarConst reads a bare Go scalar as a UConst so a store into a
// typed cell materializes it under the cell's type — `x = 300` on an
// int8 var range-checks like a constant.
func bareScalarConst(x runtime.Value) (*runtime.UConst, bool) {
	switch t := x.(type) {
	case int64:
		return &runtime.UConst{V: constant.MakeInt64(t)}, true
	case float64:
		return &runtime.UConst{V: constant.MakeFloat64(t)}, true
	case string:
		return &runtime.UConst{V: constant.MakeString(t)}, true
	case bool:
		return &runtime.UConst{V: constant.MakeBool(t)}, true
	}
	return nil, false
}

// hostConstArg converts an untyped constant passed to a host parameter of
// integer or float kind at the parameter's type, as gc does: 1<<64-1
// fits a uint64 parameter though it overflows the default int, and 300
// for an int8 parameter (or 1.5 for an int one, 1e300 for a float32 one)
// is gc's compile error rather than a silent wrap. ok is false for every
// other parameter kind, which takes the constant's default type.
func hostConstArg(u *runtime.UConst, t reflect.Type) (rv reflect.Value, ok bool, err error) {
	name, ok := hostConstKindNames[t.Kind()]
	if !ok {
		return reflect.Value{}, false, nil
	}
	cv := u.V
	if cv.Kind() == constant.Complex && constant.Sign(constant.Imag(cv)) == 0 {
		// complex(3, 0) is representable by any numeric type.
		cv = constant.Real(cv)
	}
	switch t.Kind() {
	case reflect.Float32, reflect.Float64:
		if u.Wrap && u.WrapSure {
			return reflect.Value{}, true, fmt.Errorf("invalid operation: shifted operand (type %s) must be integer", t)
		}
		f, ok := constFloat(cv)
		if !ok {
			return reflect.Value{}, true, fmt.Errorf("cannot use constant %s as %s", u.V, t)
		}
		if t.Kind() == reflect.Float32 {
			f32, _ := constant.Float32Val(cv)
			if math.IsInf(float64(f32), 0) || math.IsInf(f, 0) {
				return reflect.Value{}, true, fmt.Errorf("constant %s overflows %s", u.V, t)
			}
			f = runtime.CanonConstZero(float64(f32))
		} else if math.IsInf(f, 0) {
			return reflect.Value{}, true, fmt.Errorf("constant %s overflows %s", u.V, t)
		}
		return reflect.ValueOf(f).Convert(t), true, nil
	}
	switch cv.Kind() {
	case constant.Int:
	case constant.Float:
		if ti, ok := toIntConst(cv); !ok || ti.Kind() != constant.Int {
			return reflect.Value{}, true, fmt.Errorf("cannot use constant %s as %s (truncated)", u.V, t)
		}
	default:
		return reflect.Value{}, true, fmt.Errorf("cannot use constant %s as %s", u.V, t)
	}
	i, ok := fitsUConst(&runtime.UConst{V: cv, Wrap: u.Wrap}, name)
	if !ok {
		return reflect.Value{}, true, fmt.Errorf("constant %s overflows %s", u.V, t)
	}
	if t.Kind() >= reflect.Uint && t.Kind() <= reflect.Uintptr {
		// i carries a uint64 above MaxInt64 in its two's-complement bits.
		return reflect.ValueOf(uint64(i)).Convert(t), true, nil
	}
	return reflect.ValueOf(i).Convert(t), true, nil
}

// constTruthy evaluates a constant's truthiness without materializing —
// bool constants read directly, others compare against zero of their
// kind (constants have no -0, so -0.0 folds to +0 and reads false).
func constTruthy(u *runtime.UConst) bool {
	switch u.V.Kind() {
	case constant.Bool:
		return constant.BoolVal(u.V)
	case constant.Int, constant.Float, constant.Complex:
		return constant.Sign(u.V) != 0
	case constant.String:
		return constant.StringVal(u.V) != ""
	}
	return true
}

// materialize turns an untyped constant into its default-typed value at
// a value boundary ('a' -> rune, `1<<100` -> trap like Go's compile-time
// "constant overflows int"); any other value passes through.
func (v *VM) materialize(f *frame, x runtime.Value) runtime.Value {
	if n, ok := x.(*runtime.Named); ok {
		// a typed constant materializes through its declared type's
		// representability rules — `x := int64(1<<63)` fails like Go's
		// compile-time "constant overflows int64".
		if u, ok2 := n.V.(*runtime.UConst); ok2 {
			return v.materializeConst(f, u, n.Typ)
		}
	}
	u, ok := x.(*runtime.UConst)
	if !ok {
		return x
	}
	r, err := materializeDefault(u)
	if err != nil {
		f.trap("%s", err)
	}
	return r
}

// constPayload reads the constant under a value: the UConst itself, or
// one riding under a typedef tag (a T(c) typed constant's surface).
func constPayload(x runtime.Value) (*runtime.UConst, bool) {
	if u, ok := x.(*runtime.UConst); ok {
		return u, true
	}
	if n, ok := x.(*runtime.Named); ok {
		if u, ok2 := n.V.(*runtime.UConst); ok2 {
			return u, true
		}
	}
	return nil, false
}

// matBuiltinArgs materializes untyped constants before they cross into a
// bound host function — a bound call sees the values a Go call would
// pass (`f("x")` hands a string, not the constant token). The
// interpreter's own builtins keep UConst: append converts a constant
// element through the declared element type, real/imag keep the
// constant domain, and adaptConst still sees const-ness on script-to-
// script calls.
//
// target, when non-nil, is the host func being called: a bare constant passed
// to an integer or float parameter converts at the parameter's type, as
// gc does — strconv.FormatUint(1<<63, 10) is legal though 1<<63
// overflows the default int, and a constant the parameter cannot hold
// is reported (gc's compile error) instead of wrapping silently.
func (v *VM) matBuiltinArgs(args []runtime.Value, target any) ([]runtime.Value, error) {
	var out []runtime.Value
	var ft reflect.Type // resolved on the first bare constant
	for i, a := range args {
		var mv runtime.Value
		var err error
		if u, ok := a.(*runtime.UConst); ok && target != nil && constNeedsParamType(u) {
			if ft == nil {
				if ft = reflect.TypeOf(target); ft.Kind() != reflect.Func {
					target = nil
					ft = nil
				}
			}
			var pt reflect.Type
			if ft != nil {
				pt = hostParamType(ft, i, len(args))
			}
			if pt != nil {
				if td := hostConstParamTD(u, pt); td != nil {
					mv, err = v.materializeConstErr(u, td)
					if err != nil {
						return nil, fmt.Errorf("arg %d: %w", i, err)
					}
					if out == nil {
						out = make([]runtime.Value, len(args))
						copy(out, args)
					}
					out[i] = mv
					continue
				}
			}
		}
		if n, ok := a.(*runtime.Named); ok {
			// a converted constant T(c) materializes at T, not the
			// default type — uint64(1<<63) crosses a bound call as a
			// uint64 where the int64 default would overflow.
			u, ok2 := n.V.(*runtime.UConst)
			if !ok2 {
				continue
			}
			mv, err = v.materializeConstErr(u, n.Typ)
		} else if u, ok := a.(*runtime.UConst); ok {
			mv, err = materializeDefault(u)
		} else {
			continue
		}
		if err != nil {
			continue // let the builtin itself report on the raw token
		}
		if out == nil {
			out = make([]runtime.Value, len(args))
			copy(out, args)
		}
		out[i] = mv
	}
	if out == nil {
		return args, nil
	}
	return out, nil
}

// constNeedsParamType reports whether a bare constant's crossing can
// depend on the host parameter's type. A string or bool constant, and an
// integer in [0, 127] — exact in every integer and float type — cross
// at their default type unchanged, so the hot call path skips the
// reflect signature lookup for them.
func constNeedsParamType(u *runtime.UConst) bool {
	switch u.V.Kind() {
	case constant.String, constant.Bool:
		return false
	case constant.Int:
		if u.Wrap {
			return true
		}
		i, ok := constant.Int64Val(u.V)
		return !ok || i < 0 || i > math.MaxInt8
	}
	return true
}

// hostConstParamTDs holds one basic typedef per numeric parameter kind,
// shared so a bound call converting a constant allocates none.
var hostConstParamTDs = func() map[reflect.Kind]*runtime.TypeDef {
	m := make(map[reflect.Kind]*runtime.TypeDef, len(hostConstKindNames))
	for k, name := range hostConstKindNames {
		m[k] = &runtime.TypeDef{Name: name, Kind: runtime.KindNamedBasic}
	}
	return m
}()

// hostConstParamTD reports the basic typedef a bare constant must
// materialize at for host parameter type pt, or nil when its default
// materialization already is the value gc passes — the common case (an
// in-range integer constant for any integer parameter, a float constant
// for a float64 one), kept free of go/constant and reflect work. The
// typed path checks representability: a constant above MaxInt64 for a
// uint64 parameter, an out-of-range or fractional one, float32 rounding.
func hostConstParamTD(u *runtime.UConst, pt reflect.Type) *runtime.TypeDef {
	k := pt.Kind()
	switch k {
	case reflect.Int, reflect.Int64:
		if u.V.Kind() == constant.Int && (u.Wrap || fitsInt64Const(u.V)) {
			return nil // the default int already masks a shift's value
		}
	case reflect.Int8, reflect.Int16, reflect.Int32,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if u.V.Kind() == constant.Int && !u.Wrap {
			// in range for the parameter (and so for int64): the default
			// int carries the same value.
			if i, ok := constant.Int64Val(u.V); ok && int64FitsKind(i, k) {
				return nil
			}
		}
	case reflect.Float64:
		if !u.Wrap && (u.V.Kind() == constant.Float || u.V.Kind() == constant.Int && fitsInt64Const(u.V)) {
			return nil
		}
	case reflect.Float32:
	default:
		return nil
	}
	return hostConstParamTDs[k]
}

// int64FitsKind reports whether i is representable in integer kind k.
func int64FitsKind(i int64, k reflect.Kind) bool {
	switch k {
	case reflect.Int8:
		return i == int64(int8(i))
	case reflect.Int16:
		return i == int64(int16(i))
	case reflect.Int32:
		return i == int64(int32(i))
	case reflect.Uint8:
		return i == int64(uint8(i))
	case reflect.Uint16:
		return i == int64(uint16(i))
	case reflect.Uint32:
		return i == int64(uint32(i))
	case reflect.Uint, reflect.Uint64, reflect.Uintptr:
		return i >= 0
	}
	return true
}

// fitsInt64Const reports whether an integer constant fits int64.
func fitsInt64Const(cv constant.Value) bool {
	_, ok := constant.Int64Val(cv)
	return ok
}

// hostParamType is the type argument i of nargs binds to in a call of
// host func type ft — the variadic element past the fixed parameters —
// or nil when the call's arity does not match ft.
func hostParamType(ft reflect.Type, i, nargs int) reflect.Type {
	nin := ft.NumIn()
	if !ft.IsVariadic() {
		if nargs != nin {
			return nil
		}
		return ft.In(i)
	}
	if i < nin-1 {
		return ft.In(i)
	}
	return ft.In(nin - 1).Elem()
}

func materializeDefault(u *runtime.UConst) (runtime.Value, error) {
	if u.V.Kind() == constant.Int && u.Rune {
		if i, ok := fitsUConst(u, "rune"); ok {
			return runtime.Tag(&runtime.TypeDef{Name: "rune", Kind: runtime.KindNamedBasic}, i), nil
		}
		return nil, fmt.Errorf("constant %s overflows rune", u.V)
	}
	mv, err := runtime.UConstNative(u)
	if err != nil {
		return nil, err
	}
	if cv, ok := mv.(complex128); ok {
		// a complex constant rides as a GoValue inside the VM so it
		// stays distinct from a real script number.
		return &runtime.GoValue{V: cv}, nil
	}
	return mv, nil
}

// materializeConst converts an untyped constant for a declared target —
// Go's representability check: `const B = 1<<100` is legal but
// `var x int = B` fails with "constant ... overflows int".
//
// A successful scalar result is memoized on the constant under td (the
// same entry adaptConst keeps): a typed constant `const k uint8 = 3`
// is one shared *UConst re-materialized on every `s += k`.
func (v *VM) materializeConst(f *frame, u *runtime.UConst, td *runtime.TypeDef) runtime.Value {
	if td != nil {
		if r, hit := u.Memo(td); hit {
			return r
		}
	}
	r, err := v.materializeConstErr(u, td)
	if err != nil {
		f.trap("%s", err)
	}
	if td != nil && immutableScalar(r) {
		u.SetMemo(td, r)
	}
	return r
}

// immutableScalar reports whether a materialized constant is safe to
// share between readers: a bare or tagged scalar, never a container a
// conversion like []byte("s") produced.
func immutableScalar(x runtime.Value) bool {
	if n, ok := x.(*runtime.Named); ok {
		x = n.V
	}
	switch x := x.(type) {
	case int64, float64, string, bool:
		return true
	case *runtime.GoValue:
		switch x.V.(type) {
		case complex64, complex128:
			return true
		}
	}
	return false
}

// materializeOperandConst resolves a still-constant operand at the
// tag's declared width — a constant riding next to a runtime operand
// (`v - 8`) enters the concrete op, never the constant domain.
func (v *VM) materializeOperandConst(f *frame, x runtime.Value, td *runtime.TypeDef) runtime.Value {
	if u, ok := constPayload(x); ok {
		return runtime.Unwrap(v.materializeConst(f, u, td))
	}
	return x
}

// MaterializeConstErr converts an untyped constant to a value of type td
// by the rules a typed declaration applies; a constant that overflows td or
// is not representable in it is reported as an error. It serves callers that hold a [runtime.VMCaller]
// but no frame, such as the print/println builtins.
func (v *VM) MaterializeConstErr(u *runtime.UConst, td *runtime.TypeDef) (runtime.Value, error) {
	return v.materializeConstErr(u, td)
}

func (v *VM) materializeConstErr(u *runtime.UConst, td *runtime.TypeDef) (runtime.Value, error) {
	utd := v.peelNamed(td)
	if utd == nil || utd.Kind == runtime.KindInterface {
		// `any`/`error` slots take the constant's default type.
		return materializeDefault(u)
	}
	name := basicNameOf(utd)
	if u.V.Kind() == constant.Complex && constant.Sign(constant.Imag(u.V)) == 0 {
		switch name {
		case "complex64", "complex128", "string", "bool":
		default:
			// a complex constant with a zero imaginary part is
			// representable by an integer or float type: `const k =
			// complex(3, 0); var i int = k` binds 3.
			u = &runtime.UConst{V: constant.Real(u.V)}
		}
	}
	if u.Wrap && u.WrapSure {
		switch name {
		case "float32", "float64", "complex64", "complex128", "string":
			// a non-constant shift's untyped left operand takes this
			// type, and a shift needs an integer operand: `var f
			// float64 = 1 << n` is a gc compile error.
			return nil, fmt.Errorf("invalid operation: shifted operand (type %s) must be integer", tdName(td))
		}
	}
	var x runtime.Value
	switch name {
	case "int", "int8", "int16", "int32", "int64", "rune",
		"uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr":
		// an integer-valued float constant is int-assignable (`var i
		// int = 1e3`); a fractional one is Go's "cannot use 1.5 as int".
		if u.V.Kind() == constant.Float {
			if _, ok := toIntConst(u.V); !ok {
				return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
			}
		}
		if u.V.Kind() != constant.Int && u.V.Kind() != constant.Float {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		i, ok := fitsUConst(u, name)
		if !ok {
			return nil, fmt.Errorf("constant %s overflows %s", u.V, name)
		}
		x = i
	case "float32":
		fv, ok := constFloat(u.V)
		if !ok {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		f32, _ := constant.Float32Val(u.V)
		if math.IsInf(float64(f32), 0) || math.IsInf(fv, 0) {
			return nil, fmt.Errorf("constant %s overflows float32", u.V)
		}
		x = float64(runtime.CanonConstZero(float64(f32)))
	case "float64":
		fv, ok := constFloat(u.V)
		if !ok {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		if math.IsInf(fv, 0) {
			return nil, fmt.Errorf("constant %s overflows float64", u.V)
		}
		x = fv
	case "complex64":
		cv, ok := constComplex(u.V)
		if !ok {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		c64 := complex64(cv)
		if math.IsInf(real(complex128(c64)), 0) || math.IsInf(imag(complex128(c64)), 0) {
			return nil, fmt.Errorf("constant %s overflows complex64", u.V)
		}
		x = &runtime.GoValue{V: c64}
	case "complex128":
		cv, ok := constComplex(u.V)
		if !ok {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		x = &runtime.GoValue{V: cv}
	case "string":
		switch u.V.Kind() {
		case constant.String:
			x = constant.StringVal(u.V)
		case constant.Int:
			// int-to-string produces the rune for a valid code point
			// and U+FFFD for an out-of-range one (Go vet would flag
			// it, the conversion itself is legal). Int64Val would
			// silently fail on a wider-than-64-bit constant like
			// string(1<<100), so range-check first.
			if constant.Compare(u.V, token.LSS, constant.MakeInt64(0)) ||
				constant.Compare(u.V, token.GTR, constant.MakeInt64(utf8.MaxRune)) {
				x = "\uFFFD"
			} else {
				i, _ := constant.Int64Val(u.V)
				x = string(rune(i))
			}
		default:
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
	case "bool":
		if u.V.Kind() != constant.Bool {
			return nil, fmt.Errorf("cannot use constant %s as %s", u.V, name)
		}
		x = constant.BoolVal(u.V)
	default:
		// not a basic-name target — the constant converts through its
		// default type first: []byte("s") is []byte(string).
		dx, err := materializeDefault(u)
		if err != nil {
			return nil, err
		}
		return v.convert(td, dx)
	}
	// the tag rule from coerceConcrete applies equally: a converted const
	// keeps the declared name (and int64/float32/complex64 tag even
	// spelled bare).
	if td != nil && (declaredType(td) || sizedIntName(td.Name) || td.Name == "int64" ||
		td.Name == "float32" || td.Name == "complex64") {
		return runtime.Tag(td, x), nil
	}
	return x, nil
}

// convertConst converts an untyped constant to a numeric target. The
// result stays a constant — T(c) is a constant expression in Go, so
// `-float64(0)` materializes +0 (constants have no -0; bug434). A
// declared or differently-typed target rides as a Named-tagged constant
// until the next materialize boundary resolves it. Non-numeric
// constants and targets (string('a'), []byte("s")) materialize now.
func (v *VM) convertConst(td *runtime.TypeDef, u *runtime.UConst) (runtime.Value, error) {
	name := basicNameOf(v.peelNamed(td))
	if !numericBasicName(name) || !numericConstKind(u.V.Kind()) || u.Wrap {
		// a non-constant shift result (`T(1 << n)`) is no constant:
		// it materializes at T's width like any value conversion.
		return v.materializeConstErr(u, td)
	}
	if _, err := v.materializeConstErr(u, td); err != nil {
		return nil, err // constant not representable — Go's compile error
	}
	if !declaredType(td) && u.DefaultName() == name {
		// `int(c)`/`float64(c)` — the bare constant already
		// materializes to the target's type.
		return u, nil
	}
	// a narrower float target rounds the constant's payload: Go
	// evaluates `float32(c)` in float32 precision, so `float64(float32
	// (0.01))` reads the float32 rounding, not 0.01's float64
	// nearest. The tag keeps the constant domain; only its value is
	// narrowed (complex64 rounds each half to float32 the same way).
	// constant.Float32Val rounds the EXACT constant once — going
	// through Float64Val first would double-round tie values.
	switch name {
	case "float32":
		f32, _ := constant.Float32Val(u.V)
		u = &runtime.UConst{V: constant.MakeFloat64(float64(f32)), Rune: u.Rune}
	case "complex64":
		re, _ := constant.Float32Val(constant.Real(u.V))
		im, _ := constant.Float32Val(constant.Imag(u.V))
		u = &runtime.UConst{V: constant.BinaryOp(
			constant.MakeFloat64(float64(re)), token.ADD,
			constant.MakeImag(constant.MakeFloat64(float64(im))))}
	}
	return runtime.Tag(td, u), nil
}

// numericConstKind reports whether the constant kind participates in
// arithmetic conversions (int/float/complex — not bool/string).
func numericConstKind(k constant.Kind) bool {
	return k == constant.Int || k == constant.Float || k == constant.Complex
}

// numericBasicName reports whether a basic type name is numeric —
// the conversion target kinds a constant stays a constant under.
// The numeric names are builtinTypeName minus bool/string/error.
func numericBasicName(name string) bool {
	return builtinTypeName(name) && name != "bool" && name != "string" && name != "error"
}

// constFloat reads a numeric constant as float64; a complex constant
// with an imaginary part is not a float.
func constFloat(cv constant.Value) (float64, bool) {
	switch cv.Kind() {
	case constant.Int, constant.Float:
	default:
		return 0, false
	}
	// Float64Val's second result is exactness, not representability —
	// Go rounds an exact-but-wide constant into the type (`float64 =
	// 1e100 >> 1000` compiles, losing precision). Overflow instead
	// reports through IsInf at the caller.
	f, _ := constant.Float64Val(cv)
	return runtime.CanonConstZero(f), true
}

func constComplex(cv constant.Value) (complex128, bool) {
	switch cv.Kind() {
	case constant.Int, constant.Float, constant.Complex:
	default:
		return 0, false
	}
	return constComplexVal(cv), true
}

// constComplexVal reads a numeric constant's real/imag parts at
// float64 precision — the same conversion materialization performs.
func constComplexVal(cv constant.Value) complex128 {
	re, _ := constant.Float64Val(constant.Real(cv))
	im, _ := constant.Float64Val(constant.Imag(cv))
	return complex(runtime.CanonConstZero(re), runtime.CanonConstZero(im))
}

// wrapReduce keeps a Wrap value's payload in int64 range: reduced mod
// 2^64 (two's complement), which commutes with every op a Wrap value
// keeps folding through (+ - * & | ^ &^ <<, unary - ^) and with the
// truncation to any width up to 64 bits, so the payload stays bounded
// however long the chain or large the shift.
func wrapReduce(cv constant.Value) constant.Value {
	if cv.Kind() != constant.Int {
		return cv
	}
	if _, ok := constant.Int64Val(cv); ok {
		return cv
	}
	return constant.MakeInt64(runtime.WrapInt64(cv))
}

// wrapConstBinary settles a non-shift op where a Wrap operand (an
// untyped constant shifted by a non-constant count) meets another
// untyped constant: both still wait for the context's type. The
// modular ops fold exactly and stay Wrap — truncation to any width
// commutes with them (`1<<n + 1`); every other op (division, remainder,
// comparisons) does not, so the Wrap side settles at its default type
// int first (`1<<n == 0` compares int(1)<<n with 0). Against a typed or
// runtime operand nothing happens here: adaptConst truncates the Wrap
// side to that operand's width, which is the context Go uses.
func (v *VM) wrapConstBinary(f *frame, op bytecode.BinOp, a, b *runtime.Value) (runtime.Value, bool) {
	ua, aOK := (*a).(*runtime.UConst)
	ub, bOK := (*b).(*runtime.UConst)
	if !aOK || !bOK || (!ua.Wrap && !ub.Wrap) {
		return nil, false
	}
	switch op {
	case bytecode.BinAdd, bytecode.BinSub, bytecode.BinMul,
		bytecode.BinAnd, bytecode.BinOr, bytecode.BinXor, bytecode.BinAndNot:
		if ua.V.Kind() == constant.Int && ub.V.Kind() == constant.Int {
			if r, ok := constBinary(op, ua, ub); ok {
				if ru, isConst := r.(*runtime.UConst); isConst {
					ru.V = wrapReduce(ru.V)
					ru.Wrap = true
					ru.WrapSure = ua.WrapSure || ub.WrapSure
					return ru, true
				}
			}
		}
	}
	if ua.Wrap {
		*a = v.materialize(f, ua)
	}
	if ub.Wrap {
		*b = v.materialize(f, ub)
	}
	return nil, false
}

// constInKind reads a constant payload in the kind of the numeric
// basic type name: a typed constant `float64(7)` keeps the Int payload
// it was spelled with, yet its arithmetic is float64 arithmetic. A
// payload that does not convert (a fractional value for an int name)
// stays as is so the caller's own check reports it.
func constInKind(u *runtime.UConst, name string) *runtime.UConst {
	var cv constant.Value
	switch name {
	case "float32", "float64":
		cv = constant.ToFloat(u.V)
	case "complex64", "complex128":
		cv = constant.ToComplex(u.V)
	default:
		cv = constant.ToInt(u.V)
	}
	if cv.Kind() == constant.Unknown || cv.Kind() == u.V.Kind() {
		return u
	}
	return &runtime.UConst{V: cv, Rune: u.Rune && cv.Kind() == constant.Int}
}

// toIntConst converts an integer-valued Float constant to Int kind —
// go/constant's ToInt panics on a fractional input, so it returns ok
// instead.
func toIntConst(cv constant.Value) (i constant.Value, ok bool) {
	defer func() {
		if recover() != nil {
			i, ok = nil, false
		}
	}()
	return constant.ToInt(cv), true
}

// fitsUConst is fitsIntConst for a constant operand: a Wrap value — a
// non-constant shift's result (`1 << n`) — is not a constant, so it
// truncates to the named width like the typed shift Go runs instead of
// failing the representability check.
func fitsUConst(u *runtime.UConst, name string) (int64, bool) {
	if u.Wrap && u.V.Kind() == constant.Int {
		return maskInt(runtime.WrapInt64(u.V), name), true
	}
	return fitsIntConst(u.V, name)
}

// fitsIntConst reports whether a constant is representable as the
// named Go int type — Go's constant-to-type conversion check; an
// integer-valued Float constant is int-convertible.
func fitsIntConst(cv constant.Value, name string) (int64, bool) {
	if cv.Kind() == constant.Float {
		ti, ok := toIntConst(cv)
		if !ok || ti.Kind() != constant.Int {
			return 0, false
		}
		cv = ti
	}
	if cv.Kind() != constant.Int {
		// complex/bool/string constants are never int-representable —
		// Int64Val would panic on them.
		return 0, false
	}
	i, iok := constant.Int64Val(cv)
	switch name {
	case "int", "int64":
		return i, iok
	case "int8":
		return i, iok && i >= -128 && i <= 127
	case "int16":
		return i, iok && i >= -32768 && i <= 32767
	case "int32", "rune":
		return i, iok && i >= -2147483648 && i <= 2147483647
	case "uint", "uint64", "uintptr":
		if iok {
			return i, i >= 0
		}
		u, uok := constant.Uint64Val(cv)
		return int64(u), uok
	case "uint8", "byte":
		return i, iok && i >= 0 && i <= 255
	case "uint16":
		return i, iok && i >= 0 && i <= 65535
	case "uint32":
		return i, iok && i >= 0 && i <= 4294967295
	}
	return 0, false
}

// constBinary folds an op over two untyped constants in the go/constant
// domain. ok is false when the op isn't constant-foldable (&& || keep
// runtime short-circuit semantics) or constant evaluation itself
// rejected the operands — the caller then materializes and applies
// runtime semantics.
func constBinary(op bytecode.BinOp, ua, ub *runtime.UConst) (res runtime.Value, ok bool) {
	defer func() {
		if recover() != nil {
			res, ok = nil, false
		}
	}()
	tok, ok := binOpToken(op)
	if !ok {
		return nil, false
	}
	switch tok {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		// the stack holds runtime values, not constant.Value — a
		// constant.MakeBool here reads as a non-bool truthy in
		// `if x != c` (default case → always true).
		return constant.Compare(ua.V, tok, ub.V), true
	case token.SHL, token.SHR:
		lv := ua.V
		if lv.Kind() == constant.Float {
			// `1e100 >> 1000` is a valid untyped constant expression:
			// the float constant is integer-valued, so the shift
			// computes in the exact integer domain.
			lv = constant.ToInt(lv)
		}
		rv := ub.V
		if rv.Kind() == constant.Float {
			rv = constant.ToInt(rv)
		}
		s, ok := constant.Uint64Val(rv)
		if !ok {
			return nil, false
		}
		return &runtime.UConst{V: constant.Shift(lv, tok, uint(s)), Rune: ua.Rune || ub.Rune}, true
	case token.QUO:
		if ua.V.Kind() == constant.Int && ub.V.Kind() == constant.Int {
			// integer constants divide truncated (7/2 is 3), like
			// constValue's QUO_ASSIGN rule.
			return &runtime.UConst{V: constant.BinaryOp(ua.V, token.QUO_ASSIGN, ub.V), Rune: ua.Rune || ub.Rune}, true
		}
	}
	return &runtime.UConst{V: constant.BinaryOp(ua.V, tok, ub.V), Rune: ua.Rune || ub.Rune}, true
}

// constOf lifts a value back into the constant domain: a UConst is
// already there; a bare literal value re-wraps as one. A Named value
// stays out — it is typed, not a constant. A GoValue boxing a basic
// numeric (folded literals wider than int64 land there) lifts too.
func constOf(x runtime.Value) (*runtime.UConst, bool) {
	switch v := x.(type) {
	case *runtime.UConst:
		return v, true
	case int64:
		return &runtime.UConst{V: constant.MakeInt64(v)}, true
	case float64:
		return &runtime.UConst{V: constant.MakeFloat64(v)}, true
	case string:
		return &runtime.UConst{V: constant.MakeString(v)}, true
	case bool:
		return &runtime.UConst{V: constant.MakeBool(v)}, true
	case *runtime.GoValue:
		switch n := v.V.(type) {
		case int:
			return &runtime.UConst{V: constant.MakeInt64(int64(n))}, true
		case int64:
			return &runtime.UConst{V: constant.MakeInt64(n)}, true
		case uint64:
			return &runtime.UConst{V: constant.MakeUint64(n)}, true
		case float64:
			return &runtime.UConst{V: constant.MakeFloat64(n)}, true
		case string:
			return &runtime.UConst{V: constant.MakeString(n)}, true
		case bool:
			return &runtime.UConst{V: constant.MakeBool(n)}, true
		}
	}
	return nil, false
}

// isPlainConst reports whether x reads as a compile-time constant for
// mixed folding — bare numerics, strings, and GoValue-boxed basic
// numerics do; typed (Named) values and everything else do not.
func isPlainConst(x runtime.Value) bool {
	switch x := x.(type) {
	case int64, float64, string, bool:
		return true
	case *runtime.GoValue:
		switch x.V.(type) {
		case int, int64, uint64, float64, string, bool:
			return true
		}
	}
	return false
}

// scalarConst converts an untyped constant to the runtime type of a
// scalar operand — comparisons first convert, then compare values
// (`f float64 == hugeconst` rounds the constant, not the operand).
// scalarKey memo-keys scalarConst's bare-scalar conversions by the
// operand's kind (UConst.Memo).
type scalarKey uint8

const (
	scalarInt scalarKey = iota + 1
	scalarFloat
	scalarString
	scalarBool
)

func scalarConst(u *runtime.UConst, b runtime.Value) (runtime.Value, bool) {
	var key scalarKey
	switch b.(type) {
	case int64:
		key = scalarInt
	case float64:
		key = scalarFloat
	case string:
		key = scalarString
	case bool:
		key = scalarBool
	}
	if key == 0 {
		return scalarConstSlow(u, b)
	}
	if r, hit := u.Memo(key); hit {
		return r, true
	}
	r, ok := scalarConstSlow(u, b)
	if ok {
		u.SetMemo(key, r)
	}
	return r, ok
}

func scalarConstSlow(u *runtime.UConst, b runtime.Value) (runtime.Value, bool) {
	switch b.(type) {
	case int64:
		i, ok := fitsUConst(u, "int")
		if !ok {
			return nil, false
		}
		return i, true
	case float64:
		fv, ok := constFloat(u.V)
		if !ok || math.IsInf(fv, 0) {
			return nil, false
		}
		return fv, true
	case string:
		if u.V.Kind() != constant.String {
			return nil, false
		}
		return constant.StringVal(u.V), true
	case bool:
		if u.V.Kind() != constant.Bool {
			return nil, false
		}
		return constant.BoolVal(u.V), true
	}
	if g, ok := b.(*runtime.GoValue); ok {
		switch g.V.(type) {
		case int, int64:
			i, ok := fitsUConst(u, "int64")
			if !ok {
				return nil, false
			}
			return &runtime.GoValue{V: i}, true
		case uint64:
			i, ok := fitsUConst(u, "uint64")
			if !ok {
				return nil, false
			}
			return &runtime.GoValue{V: uint64(i)}, true
		case float64:
			fv, ok := constFloat(u.V)
			if !ok || math.IsInf(fv, 0) {
				return nil, false
			}
			return &runtime.GoValue{V: fv}, true
		case complex64:
			cv, ok := constComplex(u.V)
			if !ok {
				return nil, false
			}
			c64 := complex64(cv)
			if math.IsInf(real(complex128(c64)), 0) || math.IsInf(imag(complex128(c64)), 0) {
				return nil, false // overflows to +Inf — gc rejects
			}
			return &runtime.GoValue{V: c64}, true
		case complex128:
			cv, ok := constComplex(u.V)
			if !ok {
				return nil, false
			}
			return &runtime.GoValue{V: cv}, true
		}
	}
	return nil, false
}

// adaptConst materializes an untyped constant operand for a binary op.
// When the other operand is a named numeric, the const adopts ITS type
// (Go spec: `x op y` where x is an untyped constant representable as
// T(y) converts x to T(y) — `'a' + uint16var` is uint16 arithmetic);
// a bare scalar operand types it the same way (`k * 2e6` computes in
// int when 2e6 is exactly representable — scalarConst is that same
// operand-type conversion comparisons already use); otherwise it
// takes its default type. An unconvertible numeric const against a
// numeric operand is gc's compile reject and traps here; a
// kind-mismatched one keeps the default materialization so the
// mismatch trap reports like Go's compile error.
func (v *VM) adaptConst(f *frame, u *runtime.UConst, other runtime.Value) runtime.Value {
	if nb, ok := other.(*runtime.Named); ok {
		if r, hit := u.Memo(nb.Typ); hit {
			return r
		}
		// an untyped int/rune constant is not assignable to a string
		// type — gc rejects `ms + 'x'`; only an explicit string(x)
		// conversion produces the rune encoding. Materialize so the
		// op's own mismatch trap reports it.
		if u.V.Kind() == constant.Int && basicNameOf(v.peelNamed(nb.Typ)) == "string" {
			return v.materialize(f, u)
		}
		// adopt into the operand's declared domain — `s + 100` on
		// `type Small int8` yields a Small operand, and a constant
		// that won't fit is gc's "constant ... overflows" /
		// "cannot use" reject, the same trap a declared conversion
		// takes. Kind-mismatched consts (`v8 + "x"`) stay on the
		// materialize path so the op's own trap reports the type
		// error; non-basic underlyings (a struct type) fall through
		// to materializeDefault the same way.
		if r, err := v.materializeConstErr(u, nb.Typ); err == nil {
			u.SetMemo(nb.Typ, r)
			return r
		}
		switch u.V.Kind() {
		case constant.Int, constant.Float, constant.Complex:
			// a numeric const that won't convert to the numeric
			// operand's type is gc's compile reject — `300 - v8`,
			// `1.5 + v8`, `(1+2i) - v8` — not a value silently
			// truncated or wrapped through the default domain.
			// The underlying chain peels through named hops
			// (`type A B; type B int8` rejects too).
			if numericBasicName(basicNameOf(v.peelNamed(nb.Typ))) {
				if _, err := v.materializeConstErr(u, nb.Typ); err != nil {
					f.trap("%s", err)
				}
			}
		}
	} else if g, ok := other.(*runtime.GoValue); ok && hostNamedInt(g.V) {
		// a named host int (reflect.Kind, parse.NodeType) adopts the
		// constant into its own type — `k == 2` compares two boxed
		// host values like Go converting 2 to the operand's type.
		if r, ok := constToHostInt(u, reflect.TypeOf(g.V)); ok {
			return r
		}
	} else if s, ok := scalarConst(u, other); ok {
		return s
	} else if td := scalarOperandTypedef(other); td != nil && numericConstKind(u.V.Kind()) {
		// the same reject against bare/GoValue numeric operands —
		// `1.5 + intvar` truncates, `(1+2i) + v` can't convert, so gc
		// fails the program rather than promote the const's domain.
		if _, err := v.materializeConstErr(u, td); err != nil {
			f.trap("%s", err)
		}
	}
	return v.materialize(f, u)
}

// constToHostInt converts an integer constant to the host type t, boxed;
// false when the constant is not an integer t can represent.
func constToHostInt(u *runtime.UConst, t reflect.Type) (runtime.Value, bool) {
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

// scalarOperandTypedef maps a bare scalar or GoValue operand to the
// basic typedef an unconvertible numeric constant is checked against —
// the operand-type adoption rule applies there too (`1.5 + intvar`
// converts to int and is rejected, not promoted to float).
func scalarOperandTypedef(x runtime.Value) *runtime.TypeDef {
	switch x.(type) {
	case int64:
		return runtime.BasicTypedef("int")
	case float64:
		return runtime.BasicTypedef("float64")
	}
	if g, ok := x.(*runtime.GoValue); ok {
		switch g.V.(type) {
		case int, int64:
			return runtime.BasicTypedef("int64")
		case uint64:
			return runtime.BasicTypedef("uint64")
		case float64:
			return runtime.BasicTypedef("float64")
		case complex64:
			return runtime.BasicTypedef("complex64")
		case complex128:
			return runtime.BasicTypedef("complex128")
		}
	}
	return nil
}

// constToBasic converts a constant to a builtin numeric value by name —
// the operand-type adoption rule's conversion half.
func constToBasic(u *runtime.UConst, name string) (runtime.Value, bool) {
	switch {
	case sizedIntName(name) || name == "int" || name == "int64":
		i, ok := fitsUConst(u, name)
		if !ok {
			return nil, false
		}
		return i, true
	case name == "float32":
		fv, ok := constFloat(u.V)
		if !ok || math.IsInf(fv, 0) {
			return nil, false
		}
		// Float32Val rounds the exact constant once — float32(fv)
		// would double-round values past the float64 midpoint.
		f32, _ := constant.Float32Val(u.V)
		if math.IsInf(float64(f32), 0) {
			return nil, false
		}
		return float64(f32), true
	case name == "float64":
		fv, ok := constFloat(u.V)
		if !ok || math.IsInf(fv, 0) {
			return nil, false
		}
		return fv, true
	}
	return nil, false
}

// untypedConstArg reports whether a call argument still rides an
// untyped-constant box: a bare UConst, or one tagged only with its own
// default builtin type (`'a'` under rune). A const carrying any other
// declared or converted type — `const k T = ...`, `int8(3)` — binds
// like a value of that type instead.
func (v *VM) untypedConstArg(x runtime.Value) bool {
	u, ok := constPayload(x)
	if !ok {
		return false
	}
	if n, ok := x.(*runtime.Named); ok && n.Typ != nil {
		if n.Typ.Spec != nil {
			return false
		}
		if tagIsNamed(n.Typ) {
			name := basicNameOf(n.Typ)
			if name == "" {
				return false
			}
			// a builtin-tagged const is typed: only a tag equal to the
			// constant's own default keeps it untyped — `int8(3)`
			// binds int8, never int.
			dtd := v.typeOfValue(u)
			if dtd == nil || name != basicNameOf(dtd) {
				return false
			}
		}
	}
	return true
}

// constFits is the constant representability check across a bind
// candidate: 'a' fits float64 but 2.3 does not fit rune. An integral
// float literal (2.0) still fits an int slot.
func (v *VM) constFits(u *runtime.UConst, td *runtime.TypeDef) bool {
	name := basicNameOf(v.peelNamed(td))
	switch name {
	case "float32", "float64":
		// constToBasic reuses the operand-adoption conversion: Int and
		// Float kinds fit, Inf results do not.
		_, ok := constToBasic(u, name)
		return ok
	case "complex64", "complex128":
		k := u.V.Kind()
		return k == constant.Int || k == constant.Float || k == constant.Complex
	case "string":
		return u.V.Kind() == constant.String
	case "bool":
		return u.V.Kind() == constant.Bool
	case "int", "int8", "int16", "int32", "rune", "int64",
		"uint", "uint8", "byte", "uint16", "uint32", "uint64", "uintptr":
		// fitsIntConst reuses the declared-width range check — 200
		// does not fit int8, and an integral float (2.0) still does.
		_, ok := fitsUConst(u, name)
		return ok
	}
	return false
}

// constJoinRank ranks a constant kind for the common-default join:
// Int < Rune < Float < Complex, mirroring go/constant.BinaryOp. Bool
// and String stand alone — they only join with their own kind.
func constJoinRank(u *runtime.UConst) int {
	switch u.V.Kind() {
	case constant.Int:
		if u.Rune {
			return 1
		}
		return 0
	case constant.Float:
		return 2
	case constant.Complex:
		return 3
	case constant.Bool:
		return 4
	case constant.String:
		return 5
	}
	return -1
}

// commonConstTypedef joins the deferred untyped-constant contributors of
// one type parameter into the widest kind's default type — 'a' + 2.3 →
// float64, 3 + 'a' → rune — then checks every contributor still fits
// the winner. It returns ok=false when the constants cannot join at
// all (bool + int), which the caller reports as an inference failure;
// (nil, true) means no usable contributors and leaves the tparam
// unbound.
func (v *VM) commonConstTypedef(args []runtime.Value) (cand *runtime.TypeDef, ok bool) {
	var us []*runtime.UConst
	var winU *runtime.UConst
	top := -1
	for _, a := range args {
		u, ok := constPayload(a)
		if !ok {
			continue
		}
		if r := constJoinRank(u); r >= 0 {
			us = append(us, u)
			if r > top {
				top = r
				winU = u
			}
		}
	}
	if winU == nil {
		return nil, true
	}
	for _, u := range us {
		r := constJoinRank(u)
		// a lower-ranked numeric joins upward, but a solo family
		// (bool/string) only joins with itself.
		if r != top && (r >= 4 || top >= 4) {
			return nil, false
		}
	}
	cand = v.typeOfValue(winU)
	for _, u := range us {
		if !v.constFits(u, cand) {
			return nil, false
		}
	}
	return cand, true
}
