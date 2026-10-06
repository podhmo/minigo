package main

import (
	"fmt"
	"reflect"
)

func Pipe[T, R any]() {
	it := func(fn func(R) bool) {}
	fmt.Println(reflect.TypeOf(it).String())
}

func main() {
	Pipe[int, int]()
}
