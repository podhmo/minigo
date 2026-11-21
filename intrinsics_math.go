package minigo

import (
	"go/constant"
	"go/token"
	"math"

	"github.com/podhmo/minigo/runtime"
)

func (e *Engine) installMath(h *hostHelpers) {
	// math's float consts are untyped literals with more precision than
	// float64 — re-parse the stdlib's source literal so constant-domain
	// math (and float32 conversions) see the same value gc does.
	mathFloat := func(lit string) runtime.Value {
		return &runtime.UConst{V: constant.MakeFromLiteral(lit, token.FLOAT, 0)}
	}

	mathLn2 := constant.MakeFromLiteral("0.693147180559945309417232121458176568075500134360255254120680009", token.FLOAT, 0)

	mathLn10 := constant.MakeFromLiteral("2.30258509299404568401799145468436420760110148862877297603332790", token.FLOAT, 0)

	e.bindBound("math", map[string]runtime.Value{
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
		"Abs":                    h.fn("math.Abs", func(a []any) (any, error) { return math.Abs(floatOf(a[0])), nil }, math.Abs),
		"Ceil":                   h.fn("math.Ceil", func(a []any) (any, error) { return math.Ceil(floatOf(a[0])), nil }, math.Ceil),
		"Floor":                  h.fn("math.Floor", func(a []any) (any, error) { return math.Floor(floatOf(a[0])), nil }, math.Floor),
		"Round":                  h.fn("math.Round", func(a []any) (any, error) { return math.Round(floatOf(a[0])), nil }, math.Round),
		"RoundToEven":            h.fn("math.RoundToEven", func(a []any) (any, error) { return math.RoundToEven(floatOf(a[0])), nil }, math.RoundToEven),
		"Trunc":                  h.fn("math.Trunc", func(a []any) (any, error) { return math.Trunc(floatOf(a[0])), nil }, math.Trunc),
		"Sqrt":                   h.fn("math.Sqrt", func(a []any) (any, error) { return math.Sqrt(floatOf(a[0])), nil }, math.Sqrt),
		"Cbrt":                   h.fn("math.Cbrt", func(a []any) (any, error) { return math.Cbrt(floatOf(a[0])), nil }, math.Cbrt),
		"Hypot":                  h.fn2("math.Hypot", func(a []any) (any, error) { return math.Hypot(floatOf(a[0]), floatOf(a[1])), nil }, math.Hypot),
		"Pow":                    h.fn2("math.Pow", func(a []any) (any, error) { return math.Pow(floatOf(a[0]), floatOf(a[1])), nil }, math.Pow),
		"Pow10":                  h.fn("math.Pow10", func(a []any) (any, error) { return math.Pow10(intOf(a[0])), nil }, math.Pow10),
		"Exp":                    h.fn("math.Exp", func(a []any) (any, error) { return math.Exp(floatOf(a[0])), nil }, math.Exp),
		"Exp2":                   h.fn("math.Exp2", func(a []any) (any, error) { return math.Exp2(floatOf(a[0])), nil }, math.Exp2),
		"Expm1":                  h.fn("math.Expm1", func(a []any) (any, error) { return math.Expm1(floatOf(a[0])), nil }, math.Expm1),
		"Log":                    h.fn("math.Log", func(a []any) (any, error) { return math.Log(floatOf(a[0])), nil }, math.Log),
		"Frexp": h.fn("math.Frexp", func(a []any) (any, error) {
			frac, exp := math.Frexp(floatOf(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{frac, int64(exp)}}, nil
		}),
		"Modf": h.fn("math.Modf", func(a []any) (any, error) {
			i, frac := math.Modf(floatOf(a[0]))
			return &runtime.Tuple{Elems: []runtime.Value{i, frac}}, nil
		}),
		"Log2":            h.fn("math.Log2", func(a []any) (any, error) { return math.Log2(floatOf(a[0])), nil }, math.Log2),
		"Log10":           h.fn("math.Log10", func(a []any) (any, error) { return math.Log10(floatOf(a[0])), nil }, math.Log10),
		"Log1p":           h.fn("math.Log1p", func(a []any) (any, error) { return math.Log1p(floatOf(a[0])), nil }, math.Log1p),
		"Mod":             h.fn2("math.Mod", func(a []any) (any, error) { return math.Mod(floatOf(a[0]), floatOf(a[1])), nil }, math.Mod),
		"Remainder":       h.fn2("math.Remainder", func(a []any) (any, error) { return math.Remainder(floatOf(a[0]), floatOf(a[1])), nil }, math.Remainder),
		"Max":             h.fn2("math.Max", func(a []any) (any, error) { return math.Max(floatOf(a[0]), floatOf(a[1])), nil }, math.Max),
		"Min":             h.fn2("math.Min", func(a []any) (any, error) { return math.Min(floatOf(a[0]), floatOf(a[1])), nil }, math.Min),
		"Dim":             h.fn2("math.Dim", func(a []any) (any, error) { return math.Dim(floatOf(a[0]), floatOf(a[1])), nil }, math.Dim),
		"Sin":             h.fn("math.Sin", func(a []any) (any, error) { return math.Sin(floatOf(a[0])), nil }, math.Sin),
		"Cos":             h.fn("math.Cos", func(a []any) (any, error) { return math.Cos(floatOf(a[0])), nil }, math.Cos),
		"Tan":             h.fn("math.Tan", func(a []any) (any, error) { return math.Tan(floatOf(a[0])), nil }, math.Tan),
		"Asin":            h.fn("math.Asin", func(a []any) (any, error) { return math.Asin(floatOf(a[0])), nil }, math.Asin),
		"Acos":            h.fn("math.Acos", func(a []any) (any, error) { return math.Acos(floatOf(a[0])), nil }, math.Acos),
		"Atan":            h.fn("math.Atan", func(a []any) (any, error) { return math.Atan(floatOf(a[0])), nil }, math.Atan),
		"Atan2":           h.fn2("math.Atan2", func(a []any) (any, error) { return math.Atan2(floatOf(a[0]), floatOf(a[1])), nil }, math.Atan2),
		"Sinh":            h.fn("math.Sinh", func(a []any) (any, error) { return math.Sinh(floatOf(a[0])), nil }, math.Sinh),
		"Cosh":            h.fn("math.Cosh", func(a []any) (any, error) { return math.Cosh(floatOf(a[0])), nil }, math.Cosh),
		"Tanh":            h.fn("math.Tanh", func(a []any) (any, error) { return math.Tanh(floatOf(a[0])), nil }, math.Tanh),
		"Erf":             h.fn("math.Erf", func(a []any) (any, error) { return math.Erf(floatOf(a[0])), nil }, math.Erf),
		"Erfc":            h.fn("math.Erfc", func(a []any) (any, error) { return math.Erfc(floatOf(a[0])), nil }, math.Erfc),
		"Gamma":           h.fn("math.Gamma", func(a []any) (any, error) { return math.Gamma(floatOf(a[0])), nil }, math.Gamma),
		"Ldexp":           h.fn2("math.Ldexp", func(a []any) (any, error) { return math.Ldexp(floatOf(a[0]), intOf(a[1])), nil }, math.Ldexp),
		"Nextafter":       h.fn2("math.Nextafter", func(a []any) (any, error) { return math.Nextafter(floatOf(a[0]), floatOf(a[1])), nil }, math.Nextafter),
		"Copysign":        h.fn2("math.Copysign", func(a []any) (any, error) { return math.Copysign(floatOf(a[0]), floatOf(a[1])), nil }, math.Copysign),
		"Signbit":         h.fn("math.Signbit", func(a []any) (any, error) { return math.Signbit(floatOf(a[0])), nil }, math.Signbit),
		"Float32bits":     h.fn("math.Float32bits", func(a []any) (any, error) { return math.Float32bits(float32(floatOf(a[0]))), nil }, math.Float32bits),
		"Float64bits":     h.fn("math.Float64bits", func(a []any) (any, error) { return math.Float64bits(floatOf(a[0])), nil }, math.Float64bits),
		"Float32frombits": h.fn("math.Float32frombits", func(a []any) (any, error) { return math.Float32frombits(uint32(intOf(a[0]))), nil }, math.Float32frombits),
		"Float64frombits": h.fn("math.Float64frombits", func(a []any) (any, error) { return math.Float64frombits(uint64(intOf(a[0]))), nil }, math.Float64frombits),
		"IsNaN":           h.fn("math.IsNaN", func(a []any) (any, error) { return math.IsNaN(floatOf(a[0])), nil }, math.IsNaN),
		"IsInf":           h.fn2("math.IsInf", func(a []any) (any, error) { return math.IsInf(floatOf(a[0]), intOf(a[1])), nil }, math.IsInf),
		"NaN":             h.fn("math.NaN", func(a []any) (any, error) { return math.NaN(), nil }, math.NaN),
		"Inf":             h.fn("math.Inf", func(a []any) (any, error) { return math.Inf(intOf(a[0])), nil }, math.Inf),
	})
}
