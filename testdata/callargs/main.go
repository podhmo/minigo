package main

// callargs exercises Engine.Call argument adaptation: host Go values
// arrive as script values the way reflect-call results already do.
import "fmt"

func Len(xs []string) int { return len(xs) }
func Sum(xs []int) int {
	s := 0
	for _, x := range xs {
		s += x
	}
	return s
}
func Sum64(xs []int64) int64 {
	s := int64(0)
	for _, x := range xs {
		s += x
	}
	return s
}
func Pair(a [2]int) int            { return a[0] + a[1] }
func Bytes(b []byte) int           { return len(b) }
func Anys(xs []any) int            { return len(xs) }
func IsNil(xs []string) bool       { return xs == nil }
func Num(n int) int                { return n + 1 }
func Small(n int8) int8            { return n + 1 }
func Any(x any) string             { return fmt.Sprintf("%T", x) }
func MapGet(m map[string]int) int  { return m["k"] }
func MapLen(m map[string]int) int  { return len(m) }
func MapSet(m map[string]int) int  { m["k"] = 9; return m["k"] }
func IsNilM(m map[string]int) bool { return m == nil }
func MapSum(m map[string]int) int {
	n := 0
	for k, v := range m {
		n += len(k) + v
	}
	return n
}
func Nested(m map[string]map[string]int) int { return m["a"]["b"] }
func NestedAny(m map[string]any) int         { return m["m"].(map[string]int)["k"] }
func MapSelf(m map[string]any) string        { return fmt.Sprintf("%T", m["self"]) }
func Shared(m map[string]any) int {
	return m["a"].(map[string]int)["k"] + m["b"].(map[string]int)["k"]
}
func SliceSelf(xs []any) string    { return fmt.Sprintf("%T", xs[0]) }
func SliceMapSelf(xs []any) string { return fmt.Sprintf("%T", xs[0].(map[string]any)["s"]) }
func SliceShared(xs []any) int     { return xs[0].([]int)[0] + xs[1].([]int)[0] }

func main() {}

// Composite-element arg shapes: a host []map[string]any adapts to the
// same-structured param whichever way the element interface spells
// (any / interface{} / interface {}), and each element arrives as a
// real map — `rs[0]["k"]` indexes without a detour.
func MapSliceGet(rs []map[string]any) int          { return rs[0]["k"].(int) }
func MapSliceGetI(rs []map[string]interface{}) int { return rs[0]["k"].(int) }
func MapSliceGetS(rs []map[string]interface{}) int { return rs[0]["k"].(int) }
func MapSliceSum(rs []map[string]any) int          { return rs[0]["a"].(int) + rs[1]["b"].(int) }
func AnyElemGet(rs []any) int                      { return rs[0].(map[string]any)["k"].(int) }
func AnyIfaceElem(v interface{}) int               { return v.(map[string]any)["k"].(int) }
func DeepNest(m map[string]any) int                { return m["m"].([]map[string]any)[0]["k"].(int) }
func MapSliceSet(rs []map[string]any) int          { rs[0]["k"] = 9; return rs[0]["k"].(int) }
