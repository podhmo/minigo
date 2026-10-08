package main

import "fmt"

type W uint

func (w W) M() uint { return uint(w) }

func main() {
	var w W = 5
	*(*uint)(&w) = 7
	fmt.Println(w.M())
}
