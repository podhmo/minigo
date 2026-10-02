package main

import (
	"fmt"
	"strings"
)

func main() {
	fmt.Println(strings.IndexByte("a,b,,c", ','))
	fmt.Println(strings.IndexByte("héllo", 'l'))
	fmt.Println(strings.IndexByte("abc", 'z'))
	fmt.Println(strings.FieldsFunc("a,b;;c", func(r rune) bool { return r == ',' || r == ';' }))
	fmt.Println(strings.FieldsFunc("  a  b ", func(r rune) bool { return r == ' ' }))
	fmt.Println(strings.FieldsFunc("", func(r rune) bool { return false }))
}
