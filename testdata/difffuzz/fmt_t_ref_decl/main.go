package main

import "fmt"

// %T of &x answers the pointee's DECLARED type, and a tracked
// &-operand through a field or index ref reads it from the ref's
// declared field/element typedef — `&t.F` on an interface field
// spells *main.I, not *main.S of the stored concrete value.
type S struct{ X int }

func (s S) M() string { return "s" }

type I interface{ M() string }

type T struct {
	F I
	G S
	P *S
}

type E struct{ T }

func main() {
	t := T{F: S{1}}
	fmt.Printf("%T %T %T\n", &t.F, &t.G, &t.P)
	e := E{T: t}
	fmt.Printf("%T\n", &e.F)
	var iarr [2]I = [2]I{S{1}, S{2}}
	sl := []I{S{3}}
	var arr [3]int
	fmt.Printf("%T %T %T\n", &iarr[0], &sl[0], &arr[0])
}
