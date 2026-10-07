package main

import "fmt"

func main() {
	var x int = 300
	// constant overflows trap like Go's compile-time "constant
	// overflows int8" (int8(300), var x int8 = 300, int8(c) for a
	// named const, var b byte = 256 — all reject as minigo runtime
	// traps, which pins cannot express since gc never runs them).
	fmt.Println(int8(x))
	var b byte = 255
	fmt.Println(b)
	var m int8 = -128
	fmt.Println(-m)
	fmt.Println(int8(127), int16(-32768))
	fmt.Println(int64(1) << 10)
}
