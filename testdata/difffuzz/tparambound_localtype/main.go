package main

import (
	"fmt"
	"reflect"
)

func f[T any](x interface{}) T {
	return x.(T)
}

type MySlice []int

type Number interface {
	~int
}

type _SliceOf[E any] interface {
	~[]E
}

func _DoubleElems[S _SliceOf[E], E Number](s S) S {
	r := make(S, len(s))
	for i, v := range s {
		r[i] = v + v
	}
	return r
}

func main() {
	type large struct{ a int }
	fmt.Println(f[large](large{a: 1}).a)
	got := _DoubleElems[MySlice](MySlice{1, 2})
	want := MySlice{2, 4}
	fmt.Println(reflect.DeepEqual(got, want))
}
