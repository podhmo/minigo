package main

import (
	"fmt"
	"reflect"
)

// gc numbers every non-alias function-local type decl in package source
// order (the noder's `·gen`, SplitVargenSuffix) and spells the index
// inside an instantiation's arg list — `main.T[int;main.U[int;int]·3]`;
// the head name strips it. Aliases get no index, and the suffix
// propagates into composite arg spellings (`func(main.L0·1)`).
// (corpus typeparam/nested.go's remaining diff)
// difffuzz: a local type inside a generic function also spells its
// enclosing type args at head position (`main.L0[int]`) — a separate
// mechanism recorded in TODO.md.

func F[A any]() {
	type T[B any] struct{}
	type U[_ any] int
	type W = int // alias gets no index

	fmt.Println(reflect.TypeOf(T[U[int]]{}))
	fmt.Println(reflect.TypeOf(T[T[U[int]]]{}))
	fmt.Println(reflect.TypeOf(T[W]{}))

	fmt.Println(reflect.TypeOf(U[int](0)))
}

func main() {
	F[int]()

	type L[B any] int
	type M L[int]
	fmt.Println(reflect.TypeOf(L[int](0)))
	fmt.Println(reflect.TypeOf(M(0)))
	fmt.Println(reflect.TypeOf(struct{ f L[int] }{}))

	type G[B any] struct{}
	fmt.Println(reflect.TypeOf(G[func(L[int])]{}))
	fmt.Println(reflect.TypeOf(G[*L[int]]{}))
	fmt.Println(reflect.TypeOf(G[L[int]]{}))
}
