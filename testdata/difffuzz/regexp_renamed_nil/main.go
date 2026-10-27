package main

// The nil *regexp.Regexp regexp.Compile returns on error asserts to
// *regexp.Regexp under a renamed import too (`rx "regexp"`): the
// spelling's package name is looked up in the file's imports, not
// compared against the package clause name.

import (
	"fmt"
	"regexp"
	rx "regexp"
)

type holder struct{ R *rx.Regexp }

func compile(s string) *rx.Regexp {
	r, _ := rx.Compile(s)
	return r
}

func main() {
	r, err := rx.Compile("(")
	fmt.Println(r == nil, err != nil)
	var b any = r
	p, ok := b.(*rx.Regexp)
	fmt.Println(p == nil, ok)
	_, ok = b.(*regexp.Regexp)
	fmt.Println(ok)
	switch b.(type) {
	case *rx.Regexp:
		fmt.Println("rx.Regexp")
	default:
		fmt.Println("other")
	}
	h := holder{R: r}
	fmt.Println(h.R == nil)
	fmt.Println(compile("(") == nil)
	fmt.Printf("%T\n", b)
}
