package main

import (
	"fmt"
	"text/template/parse"
)

func main() {
	m, err := parse.Parse("x", "hello", "", "")
	if err != nil {
		panic(err)
	}
	t := m["x"]
	t.Root.Nodes[0] = parse.NewIdentifier("replacement")
	fmt.Println(t.Root.String())
}
