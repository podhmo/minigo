package main

import "fmt"

// Zero-size element addresses collapse to one location (runtime.zerobase):
// &x[1] == &x[2] for [N][0]byte and []struct{}. Distinct arrays stay distinct;
// slices share the base even across containers.
// Source: $GOROOT/test/fixedbugs/bug352.go

var x [10][0]byte
var x2 [10][0]byte

func main() {
	y := make([]struct{}, 10)
	fmt.Println(&x[1] == &x[2])  // same array
	fmt.Println(&y[1] == &y[2])  // same slice
	fmt.Println(&x[1] == &x2[1]) // distinct arrays
	y2 := make([]struct{}, 10)
	fmt.Println(&y[1] == &y2[1]) // slices share zerobase
	a := make([][0]byte, 10)
	fmt.Println(&a[0] == &x[0]) // slice elem vs array elem
	var z [10]int
	fmt.Println(&z[1] == &z[2]) // nonzero: distinct
	s2 := make([]int, 10)
	fmt.Println(&s2[1] == &s2[2]) // nonzero: distinct
}
