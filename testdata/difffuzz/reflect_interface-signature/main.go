package main

import (
	"fmt"
	"reflect"
)

type S struct{}

func (S) F(int) {}

type I interface{ F(string) }

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panic")
		}
	}()
	fmt.Println(reflect.TypeOf(S{}).Implements(reflect.TypeOf((*I)(nil)).Elem()))
}
