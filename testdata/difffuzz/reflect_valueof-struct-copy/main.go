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
	v := reflect.ValueOf(s)
	s.X = 9
	fmt.Println(v.Field(0).Int())
}
