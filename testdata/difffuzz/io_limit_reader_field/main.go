package main

import (
	"fmt"
	"io"
	"strings"
)

type rd struct{ n int }

func (rd) Read(p []byte) (int, error) { return copy(p, "xx"), nil }

func main() {
	var r io.Reader = &rd{3}
	l := io.LimitReader(r, 2).(*io.LimitedReader)
	_, ok := l.R.(*rd)
	fmt.Println("ptr", ok)
	var r2 io.Reader = rd{5}
	l2 := io.LimitReader(r2, 1).(*io.LimitedReader)
	_, ok = l2.R.(rd)
	fmt.Println("val", ok)
	var r3 io.Reader = strings.NewReader("abc")
	l3 := io.LimitReader(r3, 1).(*io.LimitedReader)
	_, ok = l3.R.(*strings.Reader)
	fmt.Println("host", ok)
	fmt.Printf("%T %T %T\n", l.R, l2.R, l3.R)
}
