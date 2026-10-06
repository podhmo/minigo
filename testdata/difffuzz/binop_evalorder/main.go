package main

import (
	"fmt"
	"strconv"
)

func f(x any) int { return 0 }

type S struct{ v int }
type A [1]int
type M map[string]int

func try(tag string, fn func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%s: %v\n", tag, r)
		}
	}()
	_ = fn()
	fmt.Println(tag, ": ok")
}

// gc evaluates the call in the right operand before reading the plain left
// operand, so inc() runs first and c reads 3: 3 + 3 = 6.
func sideEffect() int {
	c := 0
	inc := func() int { c++; return c }
	inc()
	inc()
	return c + inc()
}

// A pure left operand's reads defer to the operation point only when the
// right has ordering points of its own; conversions defer like the ops
// they wrap, while calls, type asserts, slice bounds and map literals
// materialize eagerly at operand position. Which side panics exposes
// the order.
func main() {
	fmt.Println(sideEffect())

	var x any = "str"
	s := "42"
	r := []int{1, 2}
	j, i, z := -1, 5, 0
	try("convLR", func() any { return int(s[i]) + f(r[j]) })
	try("pureLR", func() any { return int(s[i]) + r[j] })
	try("assertL", func() any { return x.(int) + f(r[j]) })
	try("assertL2", func() any { return x.(int) + r[j] })
	try("slicedx", func() any { return r[:i][0] + f(r[j]) })
	try("assertR", func() any { return r[i] + x.(int) })
	try("nestbin", func() any { return (r[j] + r[i]) + f(0) })
	try("divL", func() any { return r[0]/z + f(r[j]) })
	try("pure2", func() any { return int(r[i]) + r[j] })
	try("slit", func() any { return S{v: r[i]} == S{v: f(r[j])} })
	try("alit", func() any { return A{r[i]}[0] + f(r[j]) })
	try("slicelit", func() any { return []int{r[i]}[0] + f(r[j]) })
	try("mapidx", func() any { return map[string]int{"k": r[i]}["k"] + f(r[j]) })
	try("namedmap", func() any { return M{"k": r[i]}["k"] + f(r[j]) })
	try("itoaidx", func() any { return int(strconv.Itoa(7)[i]) + f(r[j]) })
	try("itoaidx2", func() any { return int(strconv.Itoa(7)[i]) + r[j] })
}
