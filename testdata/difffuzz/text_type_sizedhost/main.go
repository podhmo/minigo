package main

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

var (
	v_r_0 rune   = 'a'
	v_s_1 string = "hello"
)

func main() {
	fmt.Printf("%T %v\n", unicode.ToUpper(v_r_0), unicode.ToUpper(v_r_0))
	fmt.Printf("%T %v\n", unicode.ToUpper(v_r_0)+1, unicode.ToUpper(v_r_0)+1)
	r, n := utf8.DecodeRuneInString(v_s_1)
	fmt.Printf("%T %v %d\n", r, r, n)
	fmt.Printf("%T %v\n", v_s_1[0], v_s_1[0])
	fmt.Printf("%T %v\n", unicode.IsUpper(v_r_0), unicode.IsUpper(v_r_0))
}
