package main

import (
	"fmt"
	"text/template/parse"
)

func main() {
	m, err := parse.Parse("x", "hello{{.A}}", "", "")
	if err != nil {
		panic(err)
	}
	t := m["x"]
	nodes := t.Root.Nodes
	nodes[1] = parse.NewIdentifier("swapped")
	fmt.Println(t.Root.String())
	tn := t.Root.Nodes[0].(*parse.TextNode)
	b := tn.Text
	b[1] = 'E'
	fmt.Println(t.Root.String())
	for i := range t.Root.Nodes {
		t.Root.Nodes[i] = parse.NewIdentifier(fmt.Sprint("n", i))
	}
	fmt.Println(t.Root.String())
}
