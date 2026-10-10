package main

// staticOpTyp resolved a shift's static type from its COUNT operand —
// `1 << uint(max)` typed as uint, so the compile-time compare gate
// reported `code != 1<<uint(max)` as "mismatched types int and uint"
// where gc types the shift result int (an untyped left operand of a
// non-constant shift takes its default type). Shifts now resolve to
// the left operand's type (untyped -> its default), matching
// compress/flate's huffmanDecoder.init.

import "fmt"

func main() {
	max := 5
	code := 32
	s := 1 << uint(max)
	fmt.Println(code != s)
	fmt.Println(1<<uint(max) == code)
	var u uint = 4
	fmt.Println(1 << u)
}
