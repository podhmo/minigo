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
	fmt.Println(reflect.ValueOf(&s).Field(0).Int())
}
