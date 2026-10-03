package main

import (
	"fmt"
	"reflect"
)

type s struct{ X int }
type S struct{ s }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	f := reflect.TypeOf(S{}).Field(0)
	fmt.Println(f.PkgPath == "")
}
