package main

import (
	"fmt"
	"unicode/utf8"
)

type RB []byte

func main() {
	// spare capacity: the result shares the backing array — writes
	// through it are visible on a reslice of the original.
	b := make([]byte, 1, 4)
	b[0] = 'A'
	c := utf8.AppendRune(b, 'B')
	c[0] = 'X'
	fmt.Println(string(b[:2]), string(c))

	// no spare capacity: a fresh backing, the original stays put.
	d := []byte{'A'}
	e := utf8.AppendRune(d, 'B')
	e[0] = 'X'
	fmt.Println(string(d), string(e))

	// a nil slice and a multi-byte rune.
	fmt.Println(string(utf8.AppendRune(nil, '日')))

	var n RB = RB{'z'}
	fmt.Printf("%T %s\n", utf8.AppendRune(n, '!'), utf8.AppendRune(n, '!'))
}
