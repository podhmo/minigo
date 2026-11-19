package main

import "fmt"

type myErr struct{ s string }

func (e myErr) Error() string { return e.s }

type E error

func conv(v any) any { return E(v.(error)) }

func f() {
	// local decls over interface builtins
	type A any
	type L error
	n := 3
	var le error = myErr{"b"}
	fmt.Println(A(n) == n, any(n) == n, L(le) == le)
}

func main() {
	var e error = myErr{"a"}
	e2 := E(e)
	fmt.Println(e == e2, E(e) == e2, conv(e) == e)
	// interface values keep their payload key in map[any]
	m := map[any]int{e: 1}
	m[e2] = 2
	m[conv(e)] = 5
	fmt.Println(m[e], m[e2], len(m))
	// direct conversion and nested defined-over-interface
	var ee E = myErr{"a"}
	type E2 E
	fmt.Println(e == ee, ee == e2, E2(e) == e, E2(ee) == e2)
	f()
}
