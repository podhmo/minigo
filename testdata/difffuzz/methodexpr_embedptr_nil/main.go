package main

import "fmt"

type D struct{}

func (D) M() int { return 1 }

type B struct{ *D }
type A struct{ B }

func main() {
	defer func() {
		fmt.Println("recovered:", recover())
	}()
	var a A
	f := A.M
	f(a)
}
