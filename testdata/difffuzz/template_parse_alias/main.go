package main

import (
	"fmt"
	"text/template/parse"
)

// Parse nodes and node slices alias like Go values: a struct copy is
// independent, slice aliases (resliced or appended within capacity)
// share elements with the tree, and a rebound field drops the alias.

func main() {
	a := parse.TextNode{Text: []byte("a")}
	b := a
	b.Pos = 9
	fmt.Println(a.Pos, b.Pos)
	fmt.Printf("%T\n", a)

	m, err := parse.Parse("x", "a{{.X}}b", "", "")
	if err != nil {
		panic(err)
	}
	t := m["x"]
	s := t.Root.Nodes
	al := t.Root.Nodes
	s[1] = parse.NewIdentifier("new")
	fmt.Println("alias:", al[1].String(), t.Root.String())
	t.Root.Nodes[1:][0] = parse.NewIdentifier("reslice")
	fmt.Println("reslice:", t.Root.String())
	t.Root.Nodes = []parse.Node{parse.NewIdentifier("one"), parse.NewIdentifier("two"), parse.NewIdentifier("three")}
	s[1] = parse.NewIdentifier("old")
	fmt.Println("rebind:", t.Root.String(), s[1].String())

	m, err = parse.Parse("y", "a{{.X}}b", "", "")
	if err != nil {
		panic(err)
	}
	t = m["y"]
	p := t.Root.Nodes[:1]
	p = append(p, parse.NewIdentifier("append"))
	fmt.Println(t.Root.String(), p[1].String())
}
