package main

import (
	"fmt"
)

var n = 1

func main() {
	inner := struct{ F func() int }{F: func() int { return 1 }}
	fmt.Println(inner.F() == n)
}
