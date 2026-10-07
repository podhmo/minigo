package main

import (
	"fmt"
	"reflect"
)

// A function-local type declared inside a literal that closes over a
// generic function inherits the enclosing instantiation's identity:
// F[int]'s X and F[string]'s X are different types, spelled with the
// outer argument like a direct decl's `main.X[int]`.

func f[T any]() any {
	return func() any {
		type X int
		return X(0)
	}()
}

func h[U any]() any {
	return func() any {
		type G[T any] struct{ V T }
		return G[U]{}
	}()
}

func main() {
	fmt.Println(reflect.TypeOf(f[int]()) == reflect.TypeOf(f[string]()))
	fmt.Println(reflect.TypeOf(f[int]()) == reflect.TypeOf(f[int]()))
	fmt.Println(reflect.TypeOf(f[int]()), reflect.TypeOf(f[string]()))
	fmt.Println(reflect.TypeOf(h[int]()), reflect.TypeOf(h[string]()))
}
