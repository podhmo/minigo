package main

// Assignability combos: unnamed-to-unnamed, named in one direction,
// untyped constants, and interface satisfaction all bind; identical
// underlying shape is enough only when at least one side is unnamed.

import "fmt"

type MyInts []int
type NamedInt int

type M interface {
	M() string
}
type Impl struct{ N int }

func (i Impl) M() string { return fmt.Sprint(i.N) }

func main() {
	var a []byte = []uint8{1, 2} // byte IS uint8
	var s MyInts = []int{3, 4}   // named <- unnamed
	var v []int = s              // unnamed <- named
	var f float64 = 1            // untyped constant
	var m M = Impl{N: 7}         // interface satisfaction
	var n NamedInt = 9           // untyped constant to named basic

	fmt.Println(len(a), len(s), len(v))
	fmt.Println(f, m.M(), n)
}
