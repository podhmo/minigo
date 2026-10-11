package main

import "fmt"

type D int

func duo() (D, int) { return 0, 0 }

// `a, b := f()` types each name from the callee's signature —
// `a == s` is a compile error (mismatched types D and string).
func main() {
	var s string
	a, b := duo()
	fmt.Println(a == s)
	_ = b
}
