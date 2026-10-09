package main

import (
	"fmt"
	"text/template/parse"
)

func main() {
	n := parse.NodeText
	fmt.Println(n == 0)
	var x parse.NodeType = 0
	fmt.Println(n == x)
	fmt.Printf("%T\n", n)
}
