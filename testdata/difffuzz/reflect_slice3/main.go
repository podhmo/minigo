package main

import (
	"fmt"
	"reflect"
)

type NS []int

var arr = [4]int{1, 2, 3, 4}
var sl = []int{1, 2, 3, 4}
var nsl = NS{1, 2, 3, 4}
var nilsl []int

func out(v reflect.Value) string {
	return fmt.Sprintf("%v %T len=%d cap=%d", v.Interface(), v.Interface(), v.Len(), v.Cap())
}

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	fmt.Printf("%d: %v\n", i, f())
}

func main() {
	try(0, func() any { return out(reflect.ValueOf(sl).Slice3(1, 3, 4)) })
	try(1, func() any { return out(reflect.ValueOf(sl).Slice3(1, 3, 3)) }) // cap limited to k-i
	try(2, func() any { return reflect.ValueOf(sl).Slice3(3, 1, 4) })      // i>j
	try(3, func() any { return reflect.ValueOf(sl).Slice3(-1, 2, 4) })     // i<0
	try(4, func() any { return reflect.ValueOf(sl).Slice3(0, 2, 5) })      // k>cap
	try(5, func() any { return out(reflect.ValueOf(sl).Slice3(0, 4, 4)) }) // full
	try(6, func() any { return reflect.ValueOf(arr).Slice3(0, 2, 3) })     // unaddressable array
	try(7, func() any { return out(reflect.ValueOf(&arr).Elem().Slice3(0, 2, 3)) })
	try(8, func() any { return reflect.ValueOf("abcd").Slice3(0, 2, 3) }) // string
	try(9, func() any { return out(reflect.ValueOf(nsl).Slice3(1, 3, 4)) })
	try(10, func() any { return out(reflect.ValueOf(&nsl).Elem().Slice3(1, 3, 4)) })
	try(11, func() any { return out(reflect.ValueOf(nilsl).Slice3(0, 0, 0)) }) // nil slice
	try(12, func() any { return reflect.ValueOf(3).Slice3(0, 0, 0) })          // bad kind
	// reslicing a cap-clamped view stays bounded by k
	try(13, func() any {
		v := reflect.ValueOf(sl).Slice3(1, 3, 3)
		v2 := v.Slice(0, 2)
		return fmt.Sprintf("%v len=%d cap=%d", v2.Interface(), v2.Len(), v2.Cap())
	})
}
