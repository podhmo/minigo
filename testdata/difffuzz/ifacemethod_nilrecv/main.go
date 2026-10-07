package main

import "fmt"

// $GOROOT/test/fixedbugs/issue52072.go — a value-receiver method value
// formed through an interface holding nil *T binds lazily: `i.M` and
// `defer i.M()` register fine, and the *T→T wrapper panics when the
// call runs. minigo panicked while binding, so the deferred nil-deref
// fired before f's result assignment.
type I interface{ M() }
type T struct{ x int }

func (T) M() {}

var pt *T

func f() (r int) {
	defer func() { recover() }()

	var i I = pt
	defer i.M()
	r = 1
	return
}

func main() {
	fmt.Println(f())
}
