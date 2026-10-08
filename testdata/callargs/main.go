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
func Pair(a [2]int) int      { return a[0] + a[1] }
func Bytes(b []byte) int     { return len(b) }
func Anys(xs []any) int      { return len(xs) }
func IsNil(xs []string) bool { return xs == nil }
func Num(n int) int          { return n + 1 }
func Small(n int8) int8      { return n + 1 }
func Any(x any) string       { return fmt.Sprintf("%T", x) }
func MapGet(m map[string]int) int {
	return m["k"]
}

func main() {}
