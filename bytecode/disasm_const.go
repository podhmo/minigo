package bytecode

import "fmt"

func formatConst(cst any) string {
	switch v := cst.(type) {
	case string:
		return fmt.Sprintf("%q (string)", v)
	case int64, int32, int, float64, float32, bool:
		return fmt.Sprintf("%v (%T)", v, v)
	case nil:
		return "nil"
	case interface{ DisasmConst() string }:
		return v.DisasmConst()
	default:
		return fmt.Sprintf("<%T>", v)
	}
}
