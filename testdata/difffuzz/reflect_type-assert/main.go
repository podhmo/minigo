package main

import (
	"fmt"
	"reflect"
)

type MyErr struct{ msg string }

func (e MyErr) Error() string  { return e.msg }
func (e MyErr) String() string { return "S:" + e.msg }

type MyInt int

func try(i int, f func() (any, bool)) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v, ok := f()
	fmt.Printf("%d: %T %v %v\n", i, v, v, ok)
}

func main() {
	try(0, func() (any, bool) { return reflect.TypeAssert[int](reflect.ValueOf(42)) })
	try(1, func() (any, bool) { return reflect.TypeAssert[string](reflect.ValueOf(42)) })
	var e error = nil
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("2: panic:", r)
			}
		}()
		_, ok := reflect.TypeAssert[error](reflect.ValueOf(&e).Elem())
		fmt.Println("2:", ok)
	}()
	e = MyErr{"x"}
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("3: panic:", r)
			}
		}()
		v, ok := reflect.TypeAssert[error](reflect.ValueOf(&e).Elem())
		fmt.Printf("3: %T %v %v\n", v, v.(error).Error(), ok)
	}()
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println("4: panic:", r)
			}
		}()
		v, ok := reflect.TypeAssert[fmt.Stringer](reflect.ValueOf(&e).Elem())
		fmt.Printf("4: %T %v %v\n", v, v.(fmt.Stringer).String(), ok)
	}()
	var i any = 7
	try(5, func() (any, bool) { return reflect.TypeAssert[int](reflect.ValueOf(&i).Elem()) })
	try(6, func() (any, bool) { return reflect.TypeAssert[any](reflect.ValueOf(42)) })
	try(7, func() (any, bool) { return reflect.TypeAssert[MyInt](reflect.ValueOf(MyInt(3))) })
	try(8, func() (any, bool) { return reflect.TypeAssert[int](reflect.ValueOf(MyInt(3))) })
	var p *int
	try(9, func() (any, bool) { return reflect.TypeAssert[*int](reflect.ValueOf(&p).Elem()) })
	try(10, func() (any, bool) { return reflect.TypeAssert[int](reflect.Value{}) })
	var h struct{ x int }
	try(11, func() (any, bool) { return reflect.TypeAssert[int](reflect.ValueOf(h).Field(0)) })
	try(12, func() (any, bool) { return reflect.TypeAssert[[]int](reflect.ValueOf([]int{1})) })
}
