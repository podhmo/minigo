package main

import (
	"fmt"
	"reflect"
)

type A struct{ X int }
type S struct{ *A }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	f, ok := reflect.TypeOf(S{}).FieldByName("X")
	fmt.Println(ok)
	if ok {
		fmt.Println(f.Index)
	}
}
