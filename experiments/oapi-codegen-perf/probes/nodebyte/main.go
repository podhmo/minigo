package main

import (
	"fmt"
	"text/template/parse"
)

func main() {
	m, _ := parse.Parse("x", "hello", "", "")
	n := m["x"].Root.Nodes[0].(*parse.TextNode)
	n.Text[0] = 'H'
	fmt.Println(n.String())
}
