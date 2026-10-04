package main

import (
	"fmt"
	"reflect"
)

type T struct{ A, B int }

func (T) M() int { return 1 }

type I interface{ M(int) string }

func try(i int, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	f()
}

func main() {
	try(0, func() { reflect.ValueOf(T{}).Field(5) })
	try(1, func() { reflect.ValueOf(T{}).Field(-1) })
	try(2, func() { reflect.ValueOf([]int{1, 2}).Index(9) })
	try(3, func() { reflect.ValueOf([3]int{1, 2, 3}).Index(9) })
	try(4, func() { reflect.ValueOf("abc").Index(9) })
	try(5, func() { reflect.ValueOf(T{}).Method(3) })
	try(6, func() {
		m := reflect.ValueOf(T{}).Method(0)
		fmt.Println("6:", m.Type().String())
	})
	try(7, func() { reflect.TypeOf(T{}).Field(5) })
	try(8, func() { reflect.TypeOf(T{}).Method(9) })
	it := reflect.TypeOf((*I)(nil)).Elem()
	try(9, func() {
		m := it.Method(9)
		fmt.Println("9:", m.Name == "", m.Type == nil, m.Index)
	})
	try(10, func() {
		m := it.Method(0)
		fmt.Println("10:", m.Name, m.Type, m.Index)
	})
	try(11, func() {
		type Inner struct{ T }
		reflect.ValueOf(Inner{}).FieldByIndex([]int{0, 9})
	})
	try(12, func() {
		type Inner struct{ T }
		reflect.TypeOf(Inner{}).FieldByIndex([]int{0, 9})
	})
}
