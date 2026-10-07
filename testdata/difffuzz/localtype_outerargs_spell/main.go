package main

import (
	"fmt"
	"reflect"
)

// A function-local type declared inside a generic function is implicitly
// parameterized by the enclosing type arguments: gc spells it with the
// outer args everywhere — `main.L0[int]` at head position and
// `main.U[int;int]` (outer;own) inside composite arg spellings. minigo
// gives these typedefs the right identity (localtype_outerargs) but
// keeps the unspecialized name for display, so `[int]` never spells.

func F[A any]() {
	type L0 int
	type T[B any] struct{}
	type U[_ any] int

	var l0 L0
	fmt.Println("bare:", reflect.TypeOf(l0))
	fmt.Println("elem:", reflect.TypeOf(T[L0]{}))
	fmt.Println("ptr:", reflect.TypeOf(&l0))
	fmt.Println("fnarg:", reflect.TypeOf(T[func(U[int])]{}))
}

func main() { F[int]() }
