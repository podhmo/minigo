package main

import (
	"fmt"
	"reflect"
)

func try(i int, f func() any) (r any) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Printf("%d: panic: %v\n", i, p)
			r = nil
		}
	}()
	r = f()
	fmt.Printf("%d: %T %#v\n", i, r, r)
	return
}

func main() {
	s := []int{3, 4, 5}
	try(0, func() any {
		v1 := reflect.ValueOf(s).Slice(0, 1).Slice(1, 3)
		return v1.Interface()
	})
	var ns []int
	try(1, func() any {
		v := reflect.ValueOf(ns).Slice(0, 0)
		return v.Interface()
	})
	try(2, func() any {
		v := reflect.ValueOf(ns).Slice(1, 2)
		return v.Interface()
	})
}
