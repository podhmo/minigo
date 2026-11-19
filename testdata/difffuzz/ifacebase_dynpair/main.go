package main

import "fmt"

type A any
type E error
type C chan int
type N int

type myErr struct{}

func (myErr) Error() string { return "x" }

// a declared-over-interface tag is never the dynamic type: the payload's
// (type, value) pair is what map keys, assertions and method dispatch see.
func main() {
	m := map[any]int{}
	m[C(make(chan int, 1))] = 1
	var a A = C(make(chan int, 1)) // distinct chan value
	fmt.Println(m[a])
	ch := C(make(chan int, 1))
	m2 := map[any]int{ch: 2}
	var a2 A = ch
	fmt.Println(m2[a2])

	var a3 A = N(1)
	if v, ok := a3.(int); ok {
		fmt.Println("int", v)
	} else {
		fmt.Println("no")
	}
	if _, ok := a3.(N); ok {
		fmt.Println("N")
	}
	if _, ok := a3.(A); ok {
		fmt.Println("A")
	}

	var e E = myErr{}
	if _, ok := e.(E); ok {
		fmt.Println("E", e.Error())
	}
	if v, ok := e.(myErr); ok {
		fmt.Println("myErr", v.Error())
	}
	var e2 E
	fmt.Println(e2 == nil)
	if _, ok := e2.(E); ok {
		fmt.Println("nil asserts")
	}
}
