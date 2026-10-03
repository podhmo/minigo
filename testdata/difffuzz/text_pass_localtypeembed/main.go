package main

import "fmt"

func f() int {
	type X struct{ v int }
	type Y struct{ X }
	y := Y{X: X{v: 42}}
	return y.v
}

func main() {
	fmt.Println(f())
}
