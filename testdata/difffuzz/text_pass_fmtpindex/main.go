package main

import (
	"fmt"
	"strings"
)

type S struct{ N int }

func main() {
	p := &S{N: 1}
	// an indexed %p must not clobber the operand %v sees, and the
	// reverse order must not either — the rewritten spec renumbers
	// each directive to its own operand.
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%v %[1]p", p), "&{1} 0x"))
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%v %v %[2]p", p, p), "&{1} &{1} 0x"))
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%p", p), "0x"))
	fmt.Println(fmt.Sprintf("%T %[1]v", p))
	fmt.Println(fmt.Sprintf("%[2]v %[1]v", "a", "b"))
}
