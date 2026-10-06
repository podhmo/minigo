package main

import "fmt"

// A func-local type decl shadows the package type: a value of the outer
// T must NOT satisfy an assertion to the inner T.
// $GOROOT/test/fixedbugs/bug148.go.

type T struct{ a, b int }

func f(x interface{}) interface{} {
	type T struct{ a, b int }
	if x == nil {
		return T{2, 3}
	}
	return x.(T)
}

func main() {
	outer := T{5, 7}
	defer func() { fmt.Println("panic:", recover() != nil) }()
	f(outer)
}
