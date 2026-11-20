package main

import (
	"bytes"
	"fmt"
)

// A script-declared type over a host struct inherits none of the host
// methods: %v dumps the fields like gc instead of calling String().

type MyBuf bytes.Buffer

func (b MyBuf) Shout() string { return "SHOUT" }

func main() {
	var b MyBuf
	(*bytes.Buffer)(&b).WriteString("hi")
	fmt.Printf("%v\n", b)
	fmt.Printf("%v\n", &b)
	fmt.Printf("%+v\n", b)
	fmt.Printf("%#v\n", b)
	fmt.Printf("%s\n", b)
	fmt.Printf("%q\n", b)
	fmt.Println(b.Shout())
}
