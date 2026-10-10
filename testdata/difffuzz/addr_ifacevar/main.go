package main

import "fmt"

type I interface{ M() string }
type S struct{ a, b string }

func (s S) M() string { return s.a + s.b }

type T struct{ F *I }

var siVal = I(S{"a", "b"})
var jVal = siVal
var concVal = S{"c", "d"}

func main() {
	// `&` on an inferred package-level interface var builds *I — the
	// cell carries the RHS's declared type, not the contained concrete.
	t := &T{F: &siVal}
	fmt.Println(*t.F)
	fmt.Println((*t.F).M())

	// a var-copy of an interface var keeps the interface tag too.
	t2 := &T{F: &jVal}
	fmt.Println(*t2.F)

	// the same shapes hold for a function-local `var`.
	var i = I(S{"x", "y"})
	t3 := &T{F: &i}
	fmt.Println(*t3.F)
	var j = i
	t4 := &T{F: &j}
	fmt.Println(*t4.F)

	// a concrete inferred var keeps its concrete tag — `&` stays *S.
	p := &concVal
	fmt.Printf("%T %v\n", p, *p)

	// `var x = any(v)` binds interface{}: `&x` derefs to the value.
	var a = any(S{"e", "f"})
	fmt.Println(*&a)

	// a later store into the cell coerces to the stamped type:
	// siVal is I, so assigning another I-implementing value works.
	siVal = S{"g", "h"}
	fmt.Println(*t.F)
}
