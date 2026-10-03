package main

import (
	"fmt"
	"reflect"
)

type S struct{ x []int }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	s := S{x: []int{1}}
	v := reflect.ValueOf(&s).Elem().Field(0).Slice(0, 1).Index(0)
	fmt.Println(v.CanSet(), v.CanInterface())
	v.SetInt(9)
	fmt.Println(s.x[0])
}
