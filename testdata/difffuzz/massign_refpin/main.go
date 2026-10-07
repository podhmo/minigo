package main

import "fmt"

// Multi-assign resolves each target's pointer operand at phase-1
// operand evaluation: stores left-to-right must not follow pointers
// an earlier target just reseated — whether the pointer lives in a
// plain variable, a field, or an index slot.

type T struct{ X int }

func main() {
	old := &T{}
	s := struct{ P *T }{old}
	s.P, s.P.X = new(T), 7
	fmt.Println(old.X, s.P.X) // 7 0

	a := [1]*T{&T{}}
	o2 := a[0]
	a[0], a[0].X = new(T), 8
	fmt.Println(o2.X, a[0].X) // 8 0

	sl := []*T{&T{}}
	o3 := sl[0]
	sl[0], sl[0].X = &T{}, 4
	fmt.Println(o3.X, sl[0].X) // 4 0
}
