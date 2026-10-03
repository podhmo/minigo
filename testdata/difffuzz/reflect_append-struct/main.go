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
	v := reflect.Append(reflect.ValueOf([]S{}), reflect.ValueOf(&s).Elem())
	s.X = 9
	fmt.Println(v.Index(0).Field(0).Int())
}
