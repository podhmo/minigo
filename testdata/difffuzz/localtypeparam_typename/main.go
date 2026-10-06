package main

import (
	"fmt"
	"reflect"
)

type intish interface{ ~int }

func F[A intish]() {
	type T[B intish] struct{}
	var t T[int]
	fmt.Println(reflect.TypeOf(t).String())
}

func main() {
	F[int]()
}
