package main

import (
	"bytes"
	"fmt"
	"reflect"
)

type RTag struct {
	A int    `json:"a"`
	B string `json:"b,omitempty"`
}
type RNamed int

var r_struct = RTag{A: 7, B: "t"}
var r_sptr = &r_struct
var r_sslice = []RTag{r_struct}
var r_i0 = 42

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func main() {
	try(0, func() any { return reflect.TypeOf(r_struct).String() })
	try(1, func() any { return reflect.TypeOf(r_sptr).String() })
	try(2, func() any { return reflect.TypeOf(r_sslice).String() })
	try(3, func() any { return reflect.ValueOf(r_sptr).Convert(reflect.TypeOf(RNamed(5))) })
	try(4, func() any { return reflect.TypeOf(r_struct).PkgPath() })
	try(5, func() any { return reflect.TypeOf(r_i0).PkgPath() })
	try(6, func() any { return reflect.TypeOf([3]int{1}).PkgPath() })
	try(7, func() any { return reflect.TypeOf((*bytes.Buffer)(nil)).Elem().String() })
	try(8, func() any { return reflect.TypeOf((*bytes.Buffer)(nil)).Elem().PkgPath() })
	try(9, func() any {
		m := map[RTag]int{}
		var bad any = []int{1}
		m[r_struct] = 1
		_ = m
		return map[any]int{bad: 1}
	})
}
