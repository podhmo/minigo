package main

import (
	"fmt"
	"reflect"
)

func growOf[T any](len0, cap0, n int) {
	s := make([]T, len0, cap0)
	v := reflect.ValueOf(&s).Elem()
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("len=%d cap=%d grow(%d) -> panic: %v\n", len0, cap0, n, r)
		}
	}()
	v.Grow(n)
	fmt.Printf("len=%d cap=%d grow(%d) -> len=%d cap=%d\n", len0, cap0, n, v.Len(), v.Cap())
}

func main() {
	// the reported hang
	growOf[int](100, 128, 200)
	// threshold walk: small doubles, then quarters
	growOf[int](0, 0, 1)
	growOf[int](0, 0, 5)
	growOf[int](10, 16, 10)
	growOf[int](50, 64, 100)
	growOf[int](100, 128, 100)
	growOf[int](200, 256, 100)
	growOf[int](200, 256, 400)
	growOf[int](0, 300, 100)
	growOf[int](0, 512, 1)
	growOf[int](300, 512, 300)
	growOf[int](0, 0, 300)
	growOf[int](0, 0, 2000)
	// no-op cases
	growOf[int](100, 128, 10)
	growOf[int](100, 128, 0)
	growOf[int](100, 128, -5)
	// element-size dependence
	growOf[byte](100, 128, 200)
	growOf[int32](100, 128, 200)
	growOf[int64](100, 128, 200)
	growOf[string](100, 128, 200)
	growOf[any](100, 128, 200)
	growOf[*int](100, 128, 200)
	growOf[struct{}](100, 128, 200)
	growOf[struct {
		A int64
		B int64
	}](100, 128, 200)
	growOf[struct {
		A int64
		P *int
	}](100, 128, 200)
	growOf[[3]int64](10, 16, 20)
	// nil slice
	var ns []int
	vns := reflect.ValueOf(&ns).Elem()
	vns.Grow(7)
	fmt.Printf("nil grow(7) -> len=%d cap=%d nil=%v\n", vns.Len(), vns.Cap(), ns == nil)
	// unaddressable
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("unaddr grow -> panic: %v\n", r)
			}
		}()
		reflect.ValueOf([]int{1, 2, 3}).Grow(4)
	}()
	// non-slice
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("nonslice grow -> panic: %v\n", r)
			}
		}()
		i := 0
		reflect.ValueOf(&i).Elem().Grow(4)
	}()
}
