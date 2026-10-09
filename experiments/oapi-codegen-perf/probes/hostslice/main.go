package main

import (
	"fmt"
	"text/template/parse"
)

func main() {
	m, _ := parse.Parse("x", "a{{.A}}{{.B}}", "", "")
	t := m["x"]
	t.Root.Nodes[1:][0] = parse.NewIdentifier("r")
	fmt.Println(t.Root.String())
	p := &t.Root.Nodes[2]
	*p = parse.NewIdentifier("q")
	fmt.Println(t.Root.String())
}
