package main

import (
	"fmt"
	"reflect"
)

// tryEq compares two interface-held values like Go's `==`: different
// dynamic types are simply unequal; only the SAME uncomparable type
// panics.
func tryEq(a, b any) (out string) {
	defer func() {
		if r := recover(); r != nil {
			out = "panic: comparing uncomparable type"
		}
	}()
	if a == b {
		return "true"
	}
	return "false"
}

type T struct{ X int }

func main() {
	// different element types are different dynamic types — unequal,
	// never a panic.
	fmt.Println(tryEq([]int{1}, []string{"a"}))
	fmt.Println(tryEq(map[int]int{}, map[string]int{}))
	// the SAME uncomparable type still panics.
	fmt.Println(tryEq([]int{1}, []int{1}))
	fmt.Println(tryEq(map[int]int{}, map[int]int{}))
	// typed nils pair by typedef identity.
	fmt.Println(tryEq((*int)(nil), (*string)(nil)))
	fmt.Println(tryEq((*int)(nil), (*int)(nil)))
	fmt.Println(tryEq([]int(nil), (*int)(nil)))
	// anonymous struct identity covers field types, not just names.
	fmt.Println(tryEq(struct{ X int }{X: 1}, struct{ X any }{X: 1}))
	fmt.Println(tryEq(struct{ X int }{X: 1}, struct{ X int }{X: 1}))
	fmt.Println(tryEq(T{X: 1}, T{X: 1}))
	// DeepEqual agrees.
	fmt.Println(reflect.DeepEqual([]int{1}, []string{"a"}))
	fmt.Println(reflect.DeepEqual([]int{1}, []int{1}))
}
