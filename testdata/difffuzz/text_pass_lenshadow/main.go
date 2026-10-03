package main

import "fmt"

func len(a [3]int) int { return 99 }
func cap(a [3]int) int { return 98 }

func main() {
	var a [1][3]int
	// user declarations of len/cap win over the builtin — Go calls
	// these like any function, no constant fold applies.
	fmt.Println(len(a[0]))
	fmt.Println(cap(a[0]))
}
