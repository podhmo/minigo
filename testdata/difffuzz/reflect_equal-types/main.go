package main

import (
	"fmt"
	"reflect"
)

type A struct{ X int }
type B struct{ X int }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.ValueOf(A{X: 1}).Equal(reflect.ValueOf(B{X: 1})))
}
