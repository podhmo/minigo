package main

import (
	"fmt"
	"reflect"
)

type A = int

type I interface {
	F(A)
}

type S struct{}

func (S) F(int) {}

func main() {
	fmt.Println(reflect.TypeOf(S{}).Implements(reflect.TypeOf((*I)(nil)).Elem()))
}
