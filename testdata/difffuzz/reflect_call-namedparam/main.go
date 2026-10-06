package main

import (
	"fmt"
	"reflect"
)

type N int

func F(p N) int { return int(p) }

func main() {
	out := reflect.ValueOf(F).Call([]reflect.Value{reflect.ValueOf(N(7))})
	fmt.Println(out[0].Interface())
}
