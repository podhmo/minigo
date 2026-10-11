package main

import "fmt"

type D int

// `case v := <-ch` binds the channel's element type — `v == s` is a
// compile error (mismatched types D and string).
func main() {
	var s string
	ch := make(chan D, 1)
	ch <- 0
	select {
	case v := <-ch:
		fmt.Println(v == s)
	default:
	}
}
