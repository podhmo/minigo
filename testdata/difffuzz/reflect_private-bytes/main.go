package main

import (
	"fmt"
	"reflect"
)

type S struct{ p []byte }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	s := S{p: []byte{1}}
	b := reflect.ValueOf(s).Field(0).Bytes()
	b[0] = 9
	fmt.Println(s.p[0])
}
