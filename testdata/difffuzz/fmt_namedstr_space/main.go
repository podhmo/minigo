package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// fmt.Print's operand spacing keys on the reflect Kind: a named type
// whose underlying kind is string (json.Number) suppresses the space
// like a plain string does.

type MyStr string

type A string
type B A

func main() {
	var n json.Number = "1e1000"
	var err error
	fmt.Print(n, err)
	fmt.Println()
	fmt.Print(1, 2, "x", 3, err)
	fmt.Println()

	var m MyStr = "abc"
	fmt.Print(m, 1)
	fmt.Println()

	var b B = "x"
	fmt.Print(b, 1)
	fmt.Println()

	fmt.Println(fmt.Sprint(n, 1) + "|" + fmt.Sprint(1, n) + "|" + fmt.Sprint(n, "s"))

	var buf bytes.Buffer
	fmt.Fprint(&buf, n, m, 7)
	fmt.Println(buf.String())
}
