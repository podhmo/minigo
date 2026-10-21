package main

import "fmt"

// Explicit instantiations (`F[T]`) are cached per origin function, binds
// and type arguments; each block below evaluates the same instantiation
// expression repeatedly with type arguments that must not collide.

type A int
type B int
type AliasA = A

func name[T any]() string {
	var z T
	return fmt.Sprintf("%T", z)
}

func conv[T ~int](x int) T { return T(x) }

func pair[K comparable, V any](k K, v V) string { return fmt.Sprintf("%T=%T", k, v) }

type Box[T any] struct{ v T }

func (b Box[T]) Get() T { return b.v }

func (b Box[T]) Name() string { return name[T]() }

func counter[T ~int](start T) func() T {
	n := start
	return func() T {
		n++
		return n
	}
}

func is[T any](x any) bool {
	_, ok := x.(T)
	return ok
}

func localOne() (any, func(any) bool) {
	type L struct{ x int }
	return L{}, is[L]
}

func localTwo() (any, func(any) bool) {
	type L struct{ x int }
	return L{}, is[L]
}

func localGen[T any]() (any, func(any) bool) {
	type L struct{ x T }
	return L{}, is[L]
}

func main() {
	// distinct named types with one underlying type
	for i := 0; i < 2; i++ {
		fmt.Println(name[A](), name[B](), name[int](), name[AliasA]())
		fmt.Printf("%T %T %T\n", conv[A](1), conv[B](2), conv[int](3))
	}
	// identical composite types spelled at different sites share an
	// instance and still print the same
	f1 := name[[]A]
	f2 := name[[]A]
	fmt.Println(f1(), f2(), name[[]B](), name[map[A]B](), name[map[B]A]())

	// partial instantiation: K is explicit, V is inferred per call
	for i := 0; i < 2; i++ {
		fmt.Println(pair[A](1, "s"), pair[B](1, 2.5), pair[A](1, B(2)), pair[string]("k", A(1)))
	}

	// methods of generic types keep their receiver binds
	for i := 0; i < 2; i++ {
		fmt.Println(Box[A]{1}.Get(), Box[B]{2}.Name(), Box[A]{3}.Name(), Box[[]B]{}.Name())
		g := Box[A].Name
		h := Box[B].Name
		fmt.Println(g(Box[A]{}), h(Box[B]{}))
	}

	// function-local types from different scopes and instantiations
	for i := 0; i < 2; i++ {
		v1, is1 := localOne()
		v2, is2 := localTwo()
		gi, isGi := localGen[int]()
		ga, isGa := localGen[A]()
		gi2, isGi2 := localGen[int]()
		fmt.Println(is1(v1), is1(v2), is2(v1), is2(v2))
		fmt.Println(isGi(gi), isGi(gi2), isGi2(gi), isGi(ga), isGa(gi), isGa(ga), is1(gi))
		fmt.Printf("%T %T %T %T\n", v1, v2, gi, ga)
	}

	// closures from one instantiation never share captured state
	c1 := counter[A](0)
	c2 := counter[A](10)
	c3 := counter[B](100)
	for i := 0; i < 3; i++ {
		fmt.Println(c1(), c2(), c3())
	}
	mk := counter[A]
	d1, d2 := mk(5), mk(50)
	fmt.Println(d1(), d2(), d1(), d2())
}
