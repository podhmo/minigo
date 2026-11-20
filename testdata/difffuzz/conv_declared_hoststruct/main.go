package main

import (
	"bytes"
	"fmt"
)

// A declared type over a bound host struct converts both ways: the
// value form snapshots the struct and the pointer form re-tags the
// same box.

type MyBuf bytes.Buffer

func main() {
	var b bytes.Buffer
	b.WriteString("hi")
	mb := MyBuf(b)
	b.WriteString("!")
	fmt.Println(b.String())
	fmt.Printf("%T\n", mb)
	bb := bytes.Buffer(mb)
	fmt.Println(bb.Len())
	// assignment copies the struct: mutating the copy leaves mb.
	other := mb
	(*bytes.Buffer)(&other).Reset()
	fmt.Println((*bytes.Buffer)(&mb).Len())
	pmb := (*MyBuf)(bytes.NewBufferString("s"))
	fmt.Printf("%T %v\n", pmb, pmb != nil)
}
