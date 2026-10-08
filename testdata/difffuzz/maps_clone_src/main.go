package main

import (
	"fmt"
	"maps"
)

type Ext map[string]any

func main() {
	var nilm map[string]int
	fmt.Println(maps.Clone(nilm) == nil)
	m := map[string]any{"a": 1, "b": "x"}
	c := maps.Clone(m)
	delete(c, "a")
	c["z"] = true
	fmt.Println(len(m), len(c), m["a"], c["z"])
	e := Ext{"k": 1}
	ec := maps.Clone(e)
	fmt.Printf("%T %v\n", ec, ec)
}
