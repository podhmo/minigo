package main

import (
	"fmt"
	"reflect"
)

type S []int

func try(i int, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	f()
}

func main() {
	try(0, func() {
		s := make([]int, 2, 10)
		s[0], s[1] = 7, 9
		reflect.ValueOf(&s).Elem().SetCap(4)
		fmt.Println("0:", len(s), cap(s), s)
	})
	try(1, func() {
		reflect.ValueOf([]int{1, 2, 3}).SetCap(3)
	})
	try(2, func() {
		var h struct{ x []int }
		h.x = make([]int, 2, 10)
		reflect.ValueOf(h).Field(0).SetCap(4)
	})
	try(3, func() {
		i := 0
		reflect.ValueOf(&i).Elem().SetCap(1)
	})
	try(4, func() {
		s := make([]int, 2, 10)
		reflect.ValueOf(&s).Elem().SetCap(1)
	})
	try(5, func() {
		s := make([]int, 2, 10)
		reflect.ValueOf(&s).Elem().SetCap(11)
	})
	try(6, func() {
		reflect.Value{}.SetCap(1)
	})
	try(7, func() {
		s := S(make([]int, 2, 10))
		reflect.ValueOf(&s).Elem().SetCap(3)
		fmt.Printf("7: %T %d %d\n", s, len(s), cap(s))
	})
	try(8, func() {
		s := make([]int, 2, 10)
		v := reflect.ValueOf(&s).Elem()
		v.SetCap(5)
		v.SetLen(5)
		s[4] = 42
		fmt.Println("8:", len(s), cap(s), s[4], v.Index(4).Int())
	})
}
