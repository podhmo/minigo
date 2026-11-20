package main

import (
	"errors"
	"fmt"
)

type F struct{}

func (f *F) Error() string { return "f" }

var ErrF = &F{}

func main() {
	a, b := &F{}, &F{}
	// gc allocates zero-size pointees at the shared zerobase, so
	// distinct *F pointers may compare equal — errors.Is's `err ==
	// target` sees them equal.
	fmt.Println(errors.Is(a, b))
	fmt.Println(errors.Is(&F{}, &F{}))
	fmt.Println(errors.Is(ErrF, ErrF))
	fmt.Println(errors.Is(a, &F{}))
	fmt.Println(errors.Is(fmt.Errorf("w: %w", a), b))
	var ax any = struct{}{}
	m := map[any]int{&ax: 1}
	ax = 42
	fmt.Println(m[&ax]) // &ax is the interface slot — stays distinct
	var ay any = struct{}{}
	m[&ay] = 5
	fmt.Println(m[&ax], len(m))
	pa := new([0]func())
	pb := new([0]func(int))
	na := map[any]int{pa: 1}
	na[pb] = 2
	fmt.Println(len(na)) // distinct anonymous array types
	// zero-size array pointers: distinct anon types never merge,
	// the same type seen twice shares its zerobase.
	mk := func() any { const N = 1; return new([0][N]int) }
	mk2 := func() any { const N = 2; return new([0][N]int) }
	mk3 := func() any { const M = 1; return new([0][M]int) }
	mm := map[any]int{mk(): 1}
	mm[mk2()] = 2
	mm[mk3()] = 3
	fmt.Println(len(mm))
}
