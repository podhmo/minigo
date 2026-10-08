package main

import "fmt"

var indent uint = 10

// A const string used as a slice base stays a UConst on the operand
// stack — OpSlice must materialize it before bounds-checking (corpus
// fixedbugs/bug237 trapped "slice on *runtime.UConst").
func main() {
	const dots = ". . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . " +
		". . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . . "
	const n = uint(len(dots))
	i := 2 * indent
	var s string
	for ; i > n; i -= n {
		s += fmt.Sprint(dots)
	}
	s += dots[0:i]
	fmt.Println(s)
}
