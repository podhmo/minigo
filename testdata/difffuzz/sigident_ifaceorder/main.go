package main

import "fmt"

type I interface {
	M(interface {
		A()
		B()
	})
}
type T struct{}

func (T) M(interface {
	B()
	A()
}) {
}

func main() {
	var x any = T{}
	_, ok := x.(I)
	fmt.Println(ok)
}
