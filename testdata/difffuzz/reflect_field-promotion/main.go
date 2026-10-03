package main

import (
	"fmt"
	"reflect"
)

type Inner struct{ X int }
type S struct{ Inner }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.ValueOf(S{Inner: Inner{X: 1}}).FieldByName("X").IsValid())
}
