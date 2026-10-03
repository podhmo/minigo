package main

import (
	"fmt"
	"unicode"
)

// host-backed types accept positional composite literals — stdlib
// package __init__ code (encoding/xml's RangeTables) relies on it.
func main() {
	r := unicode.Range16{0x003A, 0x0046, 1}
	t := unicode.RangeTable{R16: []unicode.Range16{r}, LatinOffset: 1}
	fmt.Println(r.Lo, r.Hi, r.Stride, t.LatinOffset, len(t.R16))
}
