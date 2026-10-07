package main

import (
	"fmt"
	"strings"
	"unicode"
)

// strings.ContainsFunc with a script predicate — encoding/json's
// jsonwire.InvalidTextError.Error uses it.

func main() {
	fmt.Println(strings.ContainsFunc("abc", unicode.IsUpper), strings.ContainsFunc("aBc", func(r rune) bool { return r == 'B' }))
}
