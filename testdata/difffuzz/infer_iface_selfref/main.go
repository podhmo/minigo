package main

import "fmt"

type N[T any] interface{ M() N[T] }

type I struct{}

func (I) M() N[int] { return I{} }

func F[T any](n N[T]) int { return 7 }

func main() {
	fmt.Println(F(I{}))
}
