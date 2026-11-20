package vm

import (
	"unicode/utf8"

	"github.com/podhmo/minigo/runtime"
)

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
func stringFromInt(sx int64) string {
	if sx < 0 || sx > utf8.MaxRune {
		return "�"
	}
	return string(rune(sx))
}
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
