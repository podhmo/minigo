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

	// `r := *p` copies the pointee like a struct value: r's own offset
	// advances while p's stays, and %T spells the value type.
	sr := strings.NewReader("abc")
	rc := *sr
	rc.ReadByte()
	nb, _ := sr.ReadByte()
	fmt.Printf("%T %c\n", rc, nb)

	// `*p = *q` stores q's pointee struct into p's storage.
	pb := bytes.NewBufferString("a")
	qb := bytes.NewBufferString("b")
	*pb = *qb
	fmt.Println(pb.String(), qb.String())
}
