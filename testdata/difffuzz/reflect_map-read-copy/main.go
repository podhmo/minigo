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
	m := map[string]S{"k": {X: 1}}
	reflect.ValueOf(m).MapIndex(reflect.ValueOf("k")).Field(0).SetInt(9)
	fmt.Println(m["k"].X)
}
