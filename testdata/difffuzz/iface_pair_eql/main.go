package main

import "fmt"

type A [2]int
type B [2]int

type I interface{}

// g is interface-typed at package level — a local `var g []int`
// shadows it and must not inherit the interface mark.
var g any

func main() {
	ch := make(chan int)
	var send chan<- int = ch
	var recv <-chan int = ch
	// static == resolves by assignability: the shared channel is equal.
	fmt.Println(send == ch, recv == ch)
	// interface == compares (dynamic type, value) pairs: chan<-int and
	// chan int are different types, so every pairing below is false.
	fmt.Println(any(send) == any(ch), any(recv) == any(ch))
	fmt.Println(any(send) == ch, send == any(ch))
	a, b := A{1, 2}, B{1, 2}
	fmt.Println(any(a) == any(b), any(a) == any([2]int{1, 2}))
	fmt.Println(a == A{1, 2})
	// named interface conversions behave like any.
	fmt.Println(I(a) == I(b), I(ch) == I(recv))
	fmt.Println(any(a) != any(b), any(ch) != any(recv))
	fmt.Println(any(1) == any(1.0), any(1) == any(1))
	// a declared interface variable marks its name — `var i any` and
	// `i := any(x)` compare strict pairs too.
	var i any = A{1}
	var j any = B{1}
	fmt.Println(i == j)
	i2 := any(A{1})
	fmt.Println(i == i2)
	// a .(I) assert to an interface type is statically an interface
	// operand too.
	fmt.Println(i.(I) == j.(I))
	// a nil interface equals nil; a typed nil inside one still pairs
	// by type, so (*int)(nil) != nil.
	var n any
	var p any = (*int)(nil)
	fmt.Println(n == nil, n == i, p == nil)
	// a local decl shadowing a package-level interface name compares
	// by its own concrete type: the nil slice equals nil.
	var g []int
	fmt.Println(g == nil)
}
