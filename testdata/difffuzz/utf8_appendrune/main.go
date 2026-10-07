package main

import (
	"fmt"
	"unicode/utf8"
)

func main() {
	b := utf8.AppendRune(nil, 'é')
	b = utf8.AppendRune(b, 'x')
	b = utf8.AppendRune([]byte("<"), '世')
	fmt.Println(string(b), len(b), b)
}
