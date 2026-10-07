package main

import (
	"fmt"
	"reflect"
)

// Type arguments bind canonically through `type A = B` aliases:
// T[Alias] instantiates, spells, and compares exactly like T[B].

type Int int
type GlobalInt = Int

type T[A, B any] struct{}

func F[A any]() any {
	type L[B any] int
	return L[A](0)
}

func main() {
	fmt.Println(reflect.TypeOf(T[GlobalInt, GlobalInt]{}))
	fmt.Println(reflect.TypeOf(T[Int, Int]{}) == reflect.TypeOf(T[GlobalInt, GlobalInt]{}))
	fmt.Println(reflect.TypeOf(F[GlobalInt]()))
	fmt.Println(reflect.TypeOf(F[Int]()) == reflect.TypeOf(F[GlobalInt]()))
}
