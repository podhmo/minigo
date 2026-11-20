package main

import "fmt"

type Mapper[A, B any] func(A) B

func Apply[X, Y any](m Mapper[X, func() Y], x X) Y { return m(x)() }

func main() {
	m := Mapper[int, func() string](func(i int) func() string { return func() string { return "hi" } })
	fmt.Println(Apply(m, 7))
}
