package main

import "fmt"

type uval uint
type uval2 uval

func main() {
	// gc rejects at compile time: `**uval` and `**uval2` do not have
	// identical underlying types (the pointer elements are named and
	// distinct). minigo accepts the conversion.
	var pw *uval
	p2 := (**uval2)(&pw)
	fmt.Println(p2 == nil)
}
