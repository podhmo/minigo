package main

import (
	"fmt"
	"reflect"
	"text/template/parse"
)

const textConst = parse.NodeText

func main() {
	// constants compare, compute and convert like named ints
	n := parse.NodeText
	var zero parse.NodeType = 0
	fmt.Println(n == 0, n == zero, textConst == 0, zero == parse.NodeText)
	fmt.Println(parse.NodeText+1 == parse.NodeAction, int(parse.NodeAction), parse.NodeType(1) == parse.NodeAction)
	fmt.Println(parse.ParseComments|parse.SkipFuncCheck, parse.ParseComments == 1)
	fmt.Printf("%T %T\n", n, parse.ParseComments)
	k := reflect.Int
	var k2 reflect.Kind = 2
	fmt.Println(k == 2, k == k2, reflect.Kind(2) == reflect.Int)

	// a parsed node's Type() matches the bound constants
	m, err := parse.Parse("x", "hello{{.A}}", "", "")
	if err != nil {
		panic(err)
	}
	t := m["x"]
	fmt.Println(t.Root.Nodes[0].Type() == parse.NodeText, t.Root.Nodes[1].Type() == parse.NodeAction)

	// element writes reach the host tree
	t.Root.Nodes[1] = parse.NewIdentifier("swapped")
	fmt.Println(t.Root.String())
	nodes := t.Root.Nodes
	nodes[1] = parse.NewIdentifier("again")
	fmt.Println(t.Root.String())
	tn := t.Root.Nodes[0].(*parse.TextNode)
	tn.Text[0] = 'H'
	fmt.Println(t.Root.String())

	// New takes func maps; IsEmptyTree(nil) is true
	nt := parse.New("y", map[string]any{"f": func() string { return "ok" }})
	fmt.Println(nt.Name, parse.IsEmptyTree(nil))

	// a typed nil func is still a defined function name
	var f func()
	_, err = parse.Parse("z", "{{f}}", "", "", map[string]any{"f": f})
	fmt.Println(err)
	_, err = parse.Parse("z", "{{g}}", "", "", map[string]any{"g": nil})
	fmt.Println(err)
}
