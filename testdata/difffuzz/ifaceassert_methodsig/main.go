package main

import "fmt"

type myfloat float64

func (x myfloat) foo() float64 { return float64(x) }

func main() {
	var i interface{} = myfloat(7)
	// myfloat.foo returns float64, not int: the assertion must fail.
	_, ok := i.(interface {
		foo() int
	})
	fmt.Println(ok)
}
