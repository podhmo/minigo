package main

import (
	"fmt"
	"reflect"
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	fmt.Printf("%d: %v\n", i, f())
}

func main() {
	i := reflect.ValueOf(1)
	try(0, func() any { i.Grow(2); return "ok" })
	try(1, func() any { reflect.ValueOf([]int{1}).Grow(2); return "ok" })
	type S struct{ A int }
	s := S{}
	sv := reflect.ValueOf(&s).Elem()
	try(2, func() any { sv.Field(0).Grow(1); return "ok" })
	sl := reflect.ValueOf(&[]int{1, 2, 3}).Elem()
	try(3, func() any { sl.Grow(2); fmt.Println("cap", cap(sl.Interface().([]int))); return "ok" })
	try(4, func() any { return sl.Len() })
	var ns []int
	nv := reflect.ValueOf(&ns).Elem()
	try(5, func() any { nv.Grow(4); return nv.IsNil() })
	try(6, func() any { return nv.Slice(0, 0).Cap() })
}
