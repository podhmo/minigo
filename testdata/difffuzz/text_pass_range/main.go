package main

import "fmt"

var ncalls = 0

func getvar(p *int) *int {
	ncalls++
	return p
}

func main() {
	// `for i, x[i] = range` — per-iteration two-phase assign: x[i]
	// binds through the previous iteration's i.
	x := []rune{'a', 'b'}
	i := 1
	for i, x[i] = range "c" {
		break
	}
	fmt.Println(i, string(x))

	// `for *getvar(&a), *getvar(&b) = range` — LHS operands evaluate
	// once per live iteration, not on the exit probe.
	var a, b int
	ncalls = 0
	for *getvar(&a), *getvar(&b) = range [2]int{1, 2} {
	}
	fmt.Println(ncalls, a, b)

	// `_` and plain ident targets.
	s := 0
	for _, v := range []int{10, 20, 30} {
		s += v
	}
	fmt.Println(s)

	m := map[string]int{"a": 1, "b": 2}
	sum := 0
	var k, v string
	var iv int
	_ = v
	for k, iv = range m {
		sum += iv
	}
	fmt.Println(sum, len(k) == 1)
}
