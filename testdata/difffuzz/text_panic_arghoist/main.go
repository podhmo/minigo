package main

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	s0  = ""
	s3  = "héllo wörld"
	s5  = "key=value; k2=v2"
	s8  = "line1\nline2\n"
	s10 = "GoGoGo"
	n6  = 10
	r0  = 'a'
)

func try(i int, f func() any) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("%d: panic: %v\n", i, r)
		}
	}()
	v := f()
	fmt.Printf("%d: %T %v\n", i, v, v)
}

func id[T any](x T) T { return x }

func main() {
	try(0, func() any {
		return fmt.Sprintf("%*s|", int(s3[strings.IndexRune(s5, func() rune {
			r, _ := utf8.DecodeRuneInString(s3)
			return r
		}())]), fmt.Sprintf("%*s|", strings.IndexRune(s8, []rune(s0)[n6]), s5))
	})
	try(1, func() any {
		return fmt.Sprintf("%*s|", int(s0[1]), strings.Repeat(s0, strings.IndexRune(s10, r0)))
	})
	try(2, func() any {
		return fmt.Sprintf("%d|%d", int(s3[id(9)]), id(7))
	})
	try(3, func() any {
		return fmt.Sprintf("%d|%d", len(s3[id(9):]), id(7))
	})
}
