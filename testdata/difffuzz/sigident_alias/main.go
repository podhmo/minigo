package main

import "fmt"

type A = int

type I interface{ M(A) }
type T struct{}

func (T) M(int) {}

func main() {
	var x any = T{}
	_, ok := x.(I)
	fmt.Println(ok)
}
