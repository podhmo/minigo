package main

import "fmt"

type Deep struct{}

func (Deep) M() string { return "Deep.M" }

type A struct{ Deep }
type B struct{}

func (B) M() string { return "B.M" }

type S struct {
	A
	B
}

type S2 struct{ A }

func main() {
	var s S
	fmt.Println(s.M())
	var s2 S2
	fmt.Println(s2.M())
}
