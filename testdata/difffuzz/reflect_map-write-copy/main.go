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
	m := map[string]S{}
	reflect.ValueOf(m).SetMapIndex(reflect.ValueOf("k"), reflect.ValueOf(&s).Elem())
	s.X = 9
	fmt.Println(m["k"].X)
}
