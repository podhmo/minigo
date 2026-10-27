package main

// An interface-typed struct field or array element compares (dynamic
// type, value): S{M(1)} != S{1} in ==, in the slices/maps helpers and
// in a map[int]any value; a map[any] built by reflect.MakeMap keys by
// dynamic type too and maps.Equal meets a script-made one.

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
)

type M int
type S struct{ A any }
type T2 struct {
	A any
	B int
}

func main() {
	fmt.Println(slices.Contains([]S{{M(1)}}, S{1}), slices.Index([]S{{M(1)}}, S{1}))
	fmt.Println(slices.Equal([]S{{M(1)}}, []S{{1}}))
	fmt.Println(slices.Contains([][1]any{{M(1)}}, [1]any{1}))
	fmt.Println(maps.Equal(map[int]S{1: {M(1)}}, map[int]S{1: {1}}), maps.Equal(map[int]any{1: S{M(1)}}, map[int]any{1: S{1}}))
	fmt.Println(S{int8(1)} == S{1}, [1]any{M(2)} == [1]any{uint32(2)}, S{M(1)} == S{M(1)}, S{1} == S{1}, T2{M(1), 2} == T2{M(1), 2})
	fmt.Println(S{nil} == S{nil}, S{"a"} == S{"a"}, S{1.5} == S{1.5}, S{[1]int{1}} == S{[1]int{1}})
	rm := reflect.MakeMap(reflect.TypeOf(map[any]int{})).Interface().(map[any]int)
	rm[M(1)] = 1
	fmt.Println(maps.Equal(rm, map[any]int{M(1): 1}), maps.Equal(map[any]int{M(1): 1}, rm))
	rm[1] = 2
	fmt.Println(len(rm))
	fmt.Println(len(slices.Compact([]S{{M(1)}, {1}})))
}
