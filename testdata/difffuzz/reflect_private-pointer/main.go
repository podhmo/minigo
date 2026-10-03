package main

import (
	"fmt"
	"reflect"
)

type S struct{ p *int }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	n := 1
	s := S{p: &n}
	v := reflect.ValueOf(&s).Elem().Field(0).Elem()
	fmt.Println(v.CanSet(), v.CanInterface())
	v.SetInt(9)
	fmt.Println(n)
}
