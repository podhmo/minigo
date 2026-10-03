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
	src := []S{{X: 1}}
	dst := make([]S, 1)
	reflect.Copy(reflect.ValueOf(dst), reflect.ValueOf(src))
	src[0].X = 9
	fmt.Println(dst[0].X)
}
