package main

import "fmt"

type both struct{}

func (both) String() string { return "STRING" }
func (both) Error() string  { return "ERROR" }

func main() {
	b := both{}
	var e error = b
	fmt.Printf("%v %s %q %x\n", b, b, b, b)
	fmt.Printf("%v %s %q %x\n", e, e, e, e)
}
