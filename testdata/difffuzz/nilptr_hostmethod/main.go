package main

import (
	"bytes"
	"fmt"
)

func main() {
	var b *bytes.Buffer
	fmt.Println(b.String())

	var s fmt.Stringer = b
	fmt.Println(s == nil)
	fmt.Println(s.String())

	defer func() {
		fmt.Println("recovered:", recover())
	}()
	b.Len()
}
