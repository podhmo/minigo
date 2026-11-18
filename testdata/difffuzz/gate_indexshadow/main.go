package main

// COMPILE_TIME_FACT: a shadowed predeclared name never stamps a synthesized bind

import "fmt"

// shadows the predeclared int inside this package — a synthesized
// range-index `int` must not coerce to it
type int interface{ M() }

func main() {
	for i := range []string{"a", "b", "c"} {
		fmt.Println(i)
	}
}
