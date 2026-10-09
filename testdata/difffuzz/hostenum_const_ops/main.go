package main

import (
	"fmt"
	"reflect"
)

// A bound host enum (reflect.Kind) compares, converts and is assigned
// from untyped constants like a Go named int.

const two = 2

func main() {
	k := reflect.TypeOf(0).Kind()
	fmt.Println(k == 2, k == two, k == reflect.Int, 2 == k)
	var j reflect.Kind = 2
	fmt.Println(j == reflect.Int, j)
	fmt.Println(reflect.Kind(2) == reflect.Int, reflect.Kind(25) == reflect.Struct)
	n := 24
	fmt.Println(reflect.Kind(n) == reflect.String, reflect.Kind(n))
	fmt.Println(reflect.TypeOf("").Kind() != 2, reflect.ValueOf(1.5).Kind() == 14)
}
