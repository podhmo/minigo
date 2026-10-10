package main

import (
	"fmt"
	"unicode/utf8"
)

func main() {
	r, n := utf8.DecodeLastRune([]byte("aé"))
	fmt.Println(r == 'é', n)
	r, n = utf8.DecodeLastRune([]byte("abc"))
	fmt.Println(r == 'c', n)
	r, n = utf8.DecodeLastRune(nil)
	fmt.Println(r == utf8.RuneError, n)
}
