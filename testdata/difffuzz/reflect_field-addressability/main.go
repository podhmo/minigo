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
	v := reflect.ValueOf(S{X: 1}).Field(0)
	fmt.Println(v.CanAddr(), v.CanSet())
}
