package main

import (
	"fmt"
	"strings"
)

func main() {
	a := strings.Builder{}
	b := a
	b.WriteString("x")
	fmt.Println(a.Len(), b.Len())
}
