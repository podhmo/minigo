package main

import (
	"fmt"
	"unicode/utf8"
)

// utf8.EncodeRune(p, r) writes into the caller's []byte and returns the
// width. A too-small p panics index-out-of-range at p[width-1]; invalid
// and surrogate runes write RuneError's encoding.

func try(p []byte, r rune) (out string) {
	defer func() {
		if x := recover(); x != nil {
			out = fmt.Sprintf("panic: %v", x)
		}
	}()
	n := utf8.EncodeRune(p, r)
	return fmt.Sprintf("n=%d buf=%v", n, p)
}

func main() {
	var buf [4]byte
	fmt.Println(try(buf[:], '世'))
	fmt.Println(try(buf[:2], '世'))
	fmt.Println(try(buf[:0], 'a'))
	fmt.Println(try(buf[:], -1))
	fmt.Println(try(buf[:], 0xD800))
	fmt.Println(try(buf[:], 0x10FFFF+1))
	var s []byte
	fmt.Println(try(s, 'a'))
	fmt.Println(try(buf[:], 'a'), buf)

	// writes through a re-sliced view reach the shared backing
	b := []byte{9, 9, 9, 9}
	fmt.Println(try(b[1:3], 'é'), b)
}
