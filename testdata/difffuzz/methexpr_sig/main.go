package main

import (
	"fmt"
	"reflect"
)

type T struct{ v int }

func (t T) M(x int) int { return t.v + x }

func main() {
	f := T.M
	fmt.Printf("%T\n", f)
	fmt.Println(reflect.TypeOf(f))
	fmt.Println(f(T{5}, 3))
}
