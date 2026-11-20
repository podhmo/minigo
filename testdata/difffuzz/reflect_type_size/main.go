package main

import (
	"fmt"
	"reflect"
)

type P struct {
	A int16
	B int64
}

func main() {
	x := 1
	fmt.Println(reflect.TypeOf(&x).Elem().Size())
	fmt.Println(reflect.TypeOf("s").Size())
	fmt.Println(reflect.TypeOf([]byte{1}).Size())
	fmt.Println(reflect.TypeOf(P{1, 2}).Size())
}
