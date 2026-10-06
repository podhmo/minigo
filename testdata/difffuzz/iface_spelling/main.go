package main

import "fmt"

// `interface{}` must spell as `interface {}` in %T output, not `<nil>`.
// $GOROOT/test/fixedbugs/issue49665.go.

var x any
var y interface{}

var _ = &x == &y

func main() {
	fmt.Printf("%T\n%T\n", &x, &y)
}
