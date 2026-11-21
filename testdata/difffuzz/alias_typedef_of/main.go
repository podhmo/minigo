package main

// Aliases are transparent: a `type A = T` decl carries no identity, so
// TypeOf sees the target, assertions against either name accept, and
// struct-field tags still distinguish spelled-identical shapes.

import (
	"fmt"
	"reflect"
)

type Base struct{ N int }
type PA = *Base
type B = Base
type C = []int

type T1 = struct {
	X int `a:"1"`
}
type T2 = struct {
	X int `b:"2"`
}

func main() {
	var p PA = &Base{N: 1}
	fmt.Printf("%T %v\n", p, p.N)

	var b B = Base{N: 2}
	fmt.Printf("%T %v\n", b, b.N)

	var c C = []int{1}
	fmt.Printf("%T %v\n", c, c)

	var i any = b
	_, ok := i.(Base)
	fmt.Println("i.(Base):", ok)
	_, ok = i.(B)
	fmt.Println("i.(B):", ok)

	var j any = p
	_, ok = j.(*Base)
	fmt.Println("j.(*Base):", ok)
	_, ok = j.(PA)
	fmt.Println("j.(PA):", ok)

	fmt.Println("typeOf(B):", reflect.TypeOf(b).Name())
	fmt.Println("typeOf(C).Kind:", reflect.TypeOf(c).Kind())

	var t1 T1
	_ = T1(t1)
	var t2 T2 = struct {
		X int `b:"2"`
	}{X: 3}
	fmt.Println("t2:", t2.X)
}
