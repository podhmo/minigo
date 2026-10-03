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
	ch := make(chan S, 1)
	s := S{X: 1}
	reflect.ValueOf(ch).Send(reflect.ValueOf(&s).Elem())
	s.X = 9
	fmt.Println((<-ch).X)
}
