package main

// COMPILE_TIME_FACT: a named shadow of int never mistypes the range index

import "fmt"

// shadows predeclared int inside this package — the synthesized
// range-index `int` still names the builtin
type int uint8

func main() {
	for i := range []string{"a", "b"} {
		// comparing the index against an untyped constant tag: gc
		// answers int == int; a shadow-resolved declTyp would trap
		// "mismatched types int and int"
		switch 0 {
		case i:
			fmt.Println("first")
		default:
			fmt.Println("rest")
		}
		fmt.Println(i + 1)
	}
}
