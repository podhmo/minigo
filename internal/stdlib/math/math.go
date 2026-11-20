package math

import (
	"go/constant"
	"go/token"
	"math"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/runtime"
)

// Install binds the math intrinsics: untyped-literal constants (kept at
// constant-domain precision) and the float64 function family.
func Install(bind stdlib.Bind, h stdlib.Helpers) {
	// math's float consts are untyped literals with more precision than
	// float64 — re-parse the stdlib's source literal so constant-domain
	// math (and float32 conversions) see the same value gc does.
	mathFloat := func(lit string) runtime.Value {
		return &runtime.UConst{V: constant.MakeFromLiteral(lit, token.FLOAT, 0)}
	}

	mathLn2 := constant.MakeFromLiteral("0.693147180559945309417232121458176568075500134360255254120680009", token.FLOAT, 0)

	mathLn10 := constant.MakeFromLiteral("2.30258509299404568401799145468436420760110148862877297603332790", token.FLOAT, 0)

	bind("math", map[string]runtime.Value{
		// math's consts are all untyped — they keep constant-domain
		// precision and adapt to an operand's declared type.
		"Pi":      mathFloat("3.14159265358979323846264338327950288419716939937510582097494459"),
		"E":       mathFloat("2.71828182845904523536028747135266249775724709369995957496696763"),
		"Phi":     mathFloat("1.61803398874989484820458683436563811772030917980576286213544862"),
		"Sqrt2":   mathFloat("1.41421356237309504880168872420969807856967187537694807317667974"),
		"SqrtE":   mathFloat("1.64872127070012814684865078781416357165377610071014801157507931"),
		"SqrtPi":  mathFloat("1.77245385090551602729816748334114518279754945612238712821380779"),
		"SqrtPhi": mathFloat("1.27201964951406896425242246173749149171560804184009624861664038"),
		"Ln2":     &runtime.UConst{V: mathLn2},
		"Log2E":   &runtime.UConst{V: constant.BinaryOp(constant.MakeInt64(1), token.QUO, mathLn2)}, // 1 / Ln2
		"Ln10":    &runtime.UConst{V: mathLn10},
		"Log10E":  &runtime.UConst{V: constant.BinaryOp(constant.MakeInt64(1), token.QUO, mathLn10)}, // 1 / Ln10
		"MaxInt":  &runtime.UConst{V: constant.MakeInt64(math.MaxInt)}, "MinInt": &runtime.UConst{V: constant.MakeInt64(math.MinInt)},
		"MaxInt8": &runtime.UConst{V: constant.MakeInt64(math.MaxInt8)}, "MinInt8": &runtime.UConst{V: constant.MakeInt64(math.MinInt8)},
		"MaxInt16": &runtime.UConst{V: constant.MakeInt64(math.MaxInt16)}, "MinInt16": &runtime.UConst{V: constant.MakeInt64(math.MinInt16)},
		"MaxInt32": &runtime.UConst{V: constant.MakeInt64(math.MaxInt32)}, "MinInt32": &runtime.UConst{V: constant.MakeInt64(math.MinInt32)},
		"MaxInt64": &runtime.UConst{V: constant.MakeInt64(math.MaxInt64)}, "MinInt64": &runtime.UConst{V: constant.MakeInt64(math.MinInt64)},
		"MaxUint8":  &runtime.UConst{V: constant.MakeInt64(math.MaxUint8)},
		"MaxUint16": &runtime.UConst{V: constant.MakeInt64(math.MaxUint16)},
		"MaxUint32": &runtime.UConst{V: constant.MakeInt64(math.MaxUint32)},
		// the 64-bit ceiling constants don't fit int64 — they stay
		// untyped constants so `x << (math.MaxUint + 0.)` still
		// evaluates in the constant domain like Go's declaration.
		"MaxUint64":  &runtime.UConst{V: constant.MakeUint64(math.MaxUint64)},
		"MaxUint":    &runtime.UConst{V: constant.MakeUint64(math.MaxUint)},
		"MaxUintptr": &runtime.UConst{V: constant.MakeUint64(math.MaxUint64)},
		// the float limit consts ARE their float64 value exactly
		// (0x1p127*(1+(1-0x1p-23)) etc. are representable), so
		// MakeFloat64 reproduces the stdlib constant bit-for-bit — a
		// decimal literal would parse to a different constant.
		"MaxFloat32":             &runtime.UConst{V: constant.MakeFloat64(math.MaxFloat32)},
		"MaxFloat64":             &runtime.UConst{V: constant.MakeFloat64(math.MaxFloat64)},
		"SmallestNonzeroFloat32": &runtime.UConst{V: constant.MakeFloat64(math.SmallestNonzeroFloat32)},
		"SmallestNonzeroFloat64": &runtime.UConst{V: constant.MakeFloat64(math.SmallestNonzeroFloat64)},
		"Abs":                    h.Fn("math.Abs", func(a []any) (any, error) { return math.Abs(stdlib.FloatOf(a[0])), nil }, math.Abs),
		"Ceil":                   h.Fn("math.Ceil", func(a []any) (any, error) { return math.Ceil(stdlib.FloatOf(a[0])), nil }, math.Ceil),
		"Floor":                  h.Fn("math.Floor", func(a []any) (any, error) { return math.Floor(stdlib.FloatOf(a[0])), nil }, math.Floor),
		"Round":                  h.Fn("math.Round", func(a []any) (any, error) { return math.Round(stdlib.FloatOf(a[0])), nil }, math.Round),
		"RoundToEven":            h.Fn("math.RoundToEven", func(a []any) (any, error) { return math.RoundToEven(stdlib.FloatOf(a[0])), nil }, math.RoundToEven),
		"Trunc":                  h.Fn("math.Trunc", func(a []any) (any, error) { return math.Trunc(stdlib.FloatOf(a[0])), nil }, math.Trunc),
		"Sqrt":                   h.Fn("math.Sqrt", func(a []any) (any, error) { return math.Sqrt(stdlib.FloatOf(a[0])), nil }, math.Sqrt),
		"Cbrt":                   h.Fn("math.Cbrt", func(a []any) (any, error) { return math.Cbrt(stdlib.FloatOf(a[0])), nil }, math.Cbrt),
		"Hypot":                  h.Fn2("math.Hypot", func(a []any) (any, error) { return math.Hypot(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Hypot),
		"Pow":                    h.Fn2("math.Pow", func(a []any) (any, error) { return math.Pow(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Pow),
		"Pow10":                  h.Fn("math.Pow10", func(a []any) (any, error) { return math.Pow10(stdlib.IntOf(a[0])), nil }, math.Pow10),
		"Exp":                    h.Fn("math.Exp", func(a []any) (any, error) { return math.Exp(stdlib.FloatOf(a[0])), nil }, math.Exp),
		"Exp2":                   h.Fn("math.Exp2", func(a []any) (any, error) { return math.Exp2(stdlib.FloatOf(a[0])), nil }, math.Exp2),
		"Expm1":                  h.Fn("math.Expm1", func(a []any) (any, error) { return math.Expm1(stdlib.FloatOf(a[0])), nil }, math.Expm1),
		"Log":                    h.Fn("math.Log", func(a []any) (any, error) { return math.Log(stdlib.FloatOf(a[0])), nil }, math.Log),
		"Frexp": h.Fn("math.Frexp", func(a []any) (any, error) {
			frac, exp := math.Frexp(stdlib.FloatOf(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{frac, int64(exp)}}, nil
		}),
		"Modf": h.Fn("math.Modf", func(a []any) (any, error) {
			i, frac := math.Modf(stdlib.FloatOf(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{i, frac}}, nil
		}),
		"Log2":      h.Fn("math.Log2", func(a []any) (any, error) { return math.Log2(stdlib.FloatOf(a[0])), nil }, math.Log2),
		"Log10":     h.Fn("math.Log10", func(a []any) (any, error) { return math.Log10(stdlib.FloatOf(a[0])), nil }, math.Log10),
		"Log1p":     h.Fn("math.Log1p", func(a []any) (any, error) { return math.Log1p(stdlib.FloatOf(a[0])), nil }, math.Log1p),
		"Mod":       h.Fn2("math.Mod", func(a []any) (any, error) { return math.Mod(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Mod),
		"Remainder": h.Fn2("math.Remainder", func(a []any) (any, error) { return math.Remainder(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Remainder),
		"Max":       h.Fn2("math.Max", func(a []any) (any, error) { return math.Max(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Max),
		"Min":       h.Fn2("math.Min", func(a []any) (any, error) { return math.Min(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Min),
		"Dim":       h.Fn2("math.Dim", func(a []any) (any, error) { return math.Dim(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Dim),
		"Sin":       h.Fn("math.Sin", func(a []any) (any, error) { return math.Sin(stdlib.FloatOf(a[0])), nil }, math.Sin),
		"Sincos": h.Fn("math.Sincos", func(a []any) (any, error) {
			s, c := math.Sincos(stdlib.FloatOf(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{s, c}}, nil
		}),
		"Cos":             h.Fn("math.Cos", func(a []any) (any, error) { return math.Cos(stdlib.FloatOf(a[0])), nil }, math.Cos),
		"Tan":             h.Fn("math.Tan", func(a []any) (any, error) { return math.Tan(stdlib.FloatOf(a[0])), nil }, math.Tan),
		"Asin":            h.Fn("math.Asin", func(a []any) (any, error) { return math.Asin(stdlib.FloatOf(a[0])), nil }, math.Asin),
		"Acos":            h.Fn("math.Acos", func(a []any) (any, error) { return math.Acos(stdlib.FloatOf(a[0])), nil }, math.Acos),
		"Atan":            h.Fn("math.Atan", func(a []any) (any, error) { return math.Atan(stdlib.FloatOf(a[0])), nil }, math.Atan),
		"Atan2":           h.Fn2("math.Atan2", func(a []any) (any, error) { return math.Atan2(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Atan2),
		"Sinh":            h.Fn("math.Sinh", func(a []any) (any, error) { return math.Sinh(stdlib.FloatOf(a[0])), nil }, math.Sinh),
		"Cosh":            h.Fn("math.Cosh", func(a []any) (any, error) { return math.Cosh(stdlib.FloatOf(a[0])), nil }, math.Cosh),
		"Tanh":            h.Fn("math.Tanh", func(a []any) (any, error) { return math.Tanh(stdlib.FloatOf(a[0])), nil }, math.Tanh),
		"Erf":             h.Fn("math.Erf", func(a []any) (any, error) { return math.Erf(stdlib.FloatOf(a[0])), nil }, math.Erf),
		"Erfc":            h.Fn("math.Erfc", func(a []any) (any, error) { return math.Erfc(stdlib.FloatOf(a[0])), nil }, math.Erfc),
		"Gamma":           h.Fn("math.Gamma", func(a []any) (any, error) { return math.Gamma(stdlib.FloatOf(a[0])), nil }, math.Gamma),
		"Ldexp":           h.Fn2("math.Ldexp", func(a []any) (any, error) { return math.Ldexp(stdlib.FloatOf(a[0]), stdlib.IntOf(a[1])), nil }, math.Ldexp),
		"Nextafter":       h.Fn2("math.Nextafter", func(a []any) (any, error) { return math.Nextafter(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Nextafter),
		"Copysign":        h.Fn2("math.Copysign", func(a []any) (any, error) { return math.Copysign(stdlib.FloatOf(a[0]), stdlib.FloatOf(a[1])), nil }, math.Copysign),
		"Signbit":         h.Fn("math.Signbit", func(a []any) (any, error) { return math.Signbit(stdlib.FloatOf(a[0])), nil }, math.Signbit),
		"Float32bits":     h.Fn("math.Float32bits", func(a []any) (any, error) { return math.Float32bits(float32(stdlib.FloatOf(a[0]))), nil }, math.Float32bits),
		"Float64bits":     h.Fn("math.Float64bits", func(a []any) (any, error) { return math.Float64bits(stdlib.FloatOf(a[0])), nil }, math.Float64bits),
		"Float32frombits": h.Fn("math.Float32frombits", func(a []any) (any, error) { return math.Float32frombits(uint32(stdlib.IntOf(a[0]))), nil }, math.Float32frombits),
		"Float64frombits": h.Fn("math.Float64frombits", func(a []any) (any, error) { return math.Float64frombits(uint64(stdlib.IntOf(a[0]))), nil }, math.Float64frombits),
		"IsNaN":           h.Fn("math.IsNaN", func(a []any) (any, error) { return math.IsNaN(stdlib.FloatOf(a[0])), nil }, math.IsNaN),
		"IsInf":           h.Fn2("math.IsInf", func(a []any) (any, error) { return math.IsInf(stdlib.FloatOf(a[0]), stdlib.IntOf(a[1])), nil }, math.IsInf),
		"NaN":             h.Fn("math.NaN", func(a []any) (any, error) { return math.NaN(), nil }, math.NaN),
		"Inf":             h.Fn("math.Inf", func(a []any) (any, error) { return math.Inf(stdlib.IntOf(a[0])), nil }, math.Inf),
	})
}
