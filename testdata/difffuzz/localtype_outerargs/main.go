package main

import (
	"fmt"
	"reflect"
)

type Int int

type packT[A any] struct{}

// F declares a local generic type and a local plain type; gc treats both
// as implicitly parameterized by the enclosing instantiation's type
// arguments — L[int] inside F[int] and inside F[Int] are different
// types, and so are the two LocalInt decls.
func F[A any]() (a, b reflect.Type) {
	type L[B any] struct{}
	type LocalInt int
	return reflect.TypeOf(L[int]{}), reflect.TypeOf(LocalInt(0))
}

func main() {
	li1, lo1 := F[int]()
	li2, lo2 := F[Int]()
	li3, lo3 := F[int]()

	fmt.Println(li1 == li2) // false: L[int] differs per enclosing instantiation
	fmt.Println(li1 == li3) // true: same enclosing instantiation
	fmt.Println(lo1 == lo2) // false: local Int differs per enclosing instantiation
	fmt.Println(lo1 == lo3) // true
	// a package-level generic's identity ignores the caller's binds
	fmt.Println(reflect.TypeOf(packT[int]{}) == reflect.TypeOf(packT[int]{})) // true
}
