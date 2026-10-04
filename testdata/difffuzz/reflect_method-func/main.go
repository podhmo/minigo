package main

import (
	"fmt"
	"reflect"
)

type S struct{ N int }

func (s S) M() int { return s.N }

func try(f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic:", r)
		}
	}()
	fmt.Println(f())
}

func main() {
	try(func() any { return reflect.TypeOf(S{}).Method(0).Func.Kind() })
	try(func() any { return reflect.TypeOf(S{}).Method(0).Func.Type() })
	try(func() any {
		m, ok := reflect.TypeOf(S{}).MethodByName("M")
		if !ok {
			return "miss"
		}
		return m.Func.Kind()
	})
	try(func() any {
		m, ok := reflect.TypeOf(S{}).MethodByName("M")
		if !ok {
			return "miss"
		}
		return m.Func.Type()
	})
	try(func() any {
		v := reflect.TypeOf((*error)(nil)).Elem().Method(0).Func
		return v.Kind()
	})
	try(func() any {
		v := reflect.TypeOf((*error)(nil)).Elem().Method(0).Func
		return reflect.Indirect(v).Interface()
	})
}
