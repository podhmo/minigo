package main

import (
	"fmt"
	"reflect"
)

type S struct{ X int }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	s := S{X: 1}
	v := reflect.ValueOf(&s).Elem()
	f := v.Field(0)
	v.Set(reflect.ValueOf(S{X: 2}))
	f.SetInt(3)
	fmt.Println(s.X)
}
