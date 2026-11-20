package main

import (
	"bytes"
	"fmt"
	"strings"
)

// `*p` where p is a boxed host pointer resolves the pointee: reads box
// its storage (*T shape), writes store through the pointer, and member
// writes go to the host object.

func main() {
	r := *strings.NewReader("abc")
	fmt.Println(r.Len())

	var b bytes.Buffer
	p := &b
	(*p).WriteString("w")
	fmt.Println(b.String())

	*p = *bytes.NewBufferString("zz")
	fmt.Println(b.String())
	fmt.Println(p.Len())

	q := &p
	**q = *bytes.NewBufferString("x")
	fmt.Println(p.Len())
}
