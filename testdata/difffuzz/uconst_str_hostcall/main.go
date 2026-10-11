package main

import (
	"bytes"
	"fmt"
	"io"
)

const greeting = "héllo\n"

var suffix = "!"

func main() {
	var b bytes.Buffer
	io.WriteString(&b, greeting)
	fmt.Print(b.String())
	io.WriteString(io.Discard, greeting)
	n, _ := io.WriteString(io.Discard, greeting+suffix)
	fmt.Println(n)
}
