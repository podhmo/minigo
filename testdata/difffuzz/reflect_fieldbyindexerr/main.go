package main

import (
	"fmt"
	"reflect"
)

type Inner struct{ X int }
type Outer struct {
	*Inner
	B int
}
type Wrap struct{ A int }

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	fmt.Printf("%d: %v\n", i, f())
}

func main() {
	o := Outer{B: 5}
	v := reflect.ValueOf(&o).Elem()
	// nil embedded pointer: FieldByIndex panics, FieldByIndexErr errors
	f, err := reflect.ValueOf(o).FieldByIndexErr([]int{0, 0})
	fmt.Printf("valid=%v err=%v\n", f.IsValid(), err)
	try(0, func() any { return v.FieldByIndex([]int{0, 0}) })
	// empty index on a struct returns the value itself
	f2, err2 := reflect.ValueOf(o).FieldByIndexErr(nil)
	fmt.Printf("valid=%v err=%v\n", f2.IsValid(), err2)
	// empty index on non-struct panics on FieldByIndexErr
	try(1, func() any {
		_, e := reflect.ValueOf(3).FieldByIndexErr(nil)
		return e
	})
	// one-step index on non-struct panics on Field
	try(2, func() any {
		_, e := reflect.ValueOf(3).FieldByIndexErr([]int{0})
		return e
	})
	// bad index still panics
	try(3, func() any {
		_, e := reflect.ValueOf(o).FieldByIndexErr([]int{9})
		return e
	})
	// happy path through live embedded ptr
	o2 := Outer{Inner: &Inner{X: 7}, B: 8}
	f3, err3 := reflect.ValueOf(o2).FieldByIndexErr([]int{0, 0})
	fmt.Printf("%v %v\n", f3.Interface(), err3)
	// single-depth via Err on plain struct
	w := Wrap{A: 9}
	f4, _ := reflect.ValueOf(w).FieldByIndexErr([]int{0})
	fmt.Println(f4.Interface())
	// err is nil-able
	f5, err5 := reflect.ValueOf(o2).FieldByIndexErr([]int{1})
	fmt.Printf("%v %v\n", f5.Interface(), err5 == nil)
}
