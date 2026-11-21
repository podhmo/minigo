package main

import (
	"bytes"
	"fmt"
)

// gc rejects a typed arg crossing a host method call: `cannot use c
// (variable of type int8) as byte value in argument to b.WriteByte`.
func main() {
	var b bytes.Buffer
	fmt.Println("start")
	var c int8 = 65
	b.WriteByte(c)
	fmt.Println(b.String())
}
