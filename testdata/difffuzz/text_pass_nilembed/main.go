package main

import "fmt"

type D struct{}

func (d D) h() int { return 1 }

type B struct{ *D }
type A struct{ B }

func main() {
	defer func() {
		fmt.Println(recover() != nil)
	}()
	var a A
	fmt.Println(a.h())
}
