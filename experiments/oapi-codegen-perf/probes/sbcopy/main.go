package main

import (
	"bytes"
	"fmt"
)

func main() {
	var a bytes.Buffer
	b := a
	b.WriteString("x")
	fmt.Println(a.Len(), b.Len())
}
