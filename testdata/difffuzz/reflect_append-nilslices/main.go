package main

import (
	"fmt"
	"reflect"
)

type RNS []int

// reflect.Append/AppendSlice/Copy on nil and named script slices used to
// trap 'reflect.Append on slice' — the operands never normalized to a
// slice header. A nil slice appends into fresh backing, a named slice
// keeps its declared-type tag, and a nil copy operand transfers zero
// elements.
func main() {
	// nil slice destination: append grows fresh backing.
	t := reflect.TypeOf([]int(nil))
	v0 := reflect.Zero(t)
	v1 := reflect.Append(v0, reflect.ValueOf(1), reflect.ValueOf(2))
	fmt.Printf("%v %T\n", v1.Interface(), v1.Interface())

	// named slice keeps its tag through Append and AppendSlice.
	var ns RNS = RNS{1}
	nv := reflect.ValueOf(&ns).Elem()
	out := reflect.Append(nv, reflect.ValueOf(9))
	fmt.Printf("%v %T\n", out.Interface(), out.Interface())
	v2 := reflect.AppendSlice(nv, reflect.ValueOf(RNS{7, 8}))
	fmt.Printf("%v %T\n", v2.Interface(), v2.Interface())

	// nil src appends nothing.
	v3 := reflect.AppendSlice(nv, v0)
	fmt.Printf("%v\n", v3.Interface())

	// Copy to/from nil slices transfers zero elements; named src works.
	var dst []int
	fmt.Println(reflect.Copy(reflect.ValueOf(&dst).Elem(), v0))
	fmt.Println(reflect.Copy(v0, reflect.ValueOf(ns)))

	// Copy into an addressable array writes through the backing.
	var big [4]int
	av := reflect.ValueOf(&big).Elem()
	fmt.Println(reflect.Copy(av, reflect.ValueOf(RNS{1, 2, 3})))
	fmt.Println(big)
}
