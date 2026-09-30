package main

import (
	greet "github.com/podhmo/minigo/testdata/greet"
)

// B before A textually: Go initializes by dependency, not order.
var B = A + 1
var A = 1

// Import use inside a package-level initializer.
var G = greet.Hello("x")

func Dep() int { return B }

func ImportInit() string { return G }

// continue before post-statement must not crash.
func ForContinue() int {
	sum := 0
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			continue
		}
		sum += i
	}
	return sum
}

// single-var range yields the index.
func RangeOne() int {
	s := []int{10, 20, 30}
	acc := 0
	for i := range s {
		acc = acc*10 + i
	}
	return acc
}

// := on an existing same-block name assigns rather than redeclaring.
func Redefine() int {
	x := 1
	_ = &x
	x, y := 5, 6
	return x*10 + y
}

// struct assignment copies by value.
func StructCopy() int {
	a := Pair{X: 1, Y: 2}
	b := a
	b.X = 99
	return a.X
}

type Pair struct {
	X int
	Y int
}

func main() {}
