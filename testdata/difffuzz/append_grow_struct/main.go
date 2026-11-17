package main

import (
	"fmt"
	"slices"
)

type Rec struct {
	Name string
	N    int
}

// A grown slice's new backing array copies every element (gc's
// growslice memcpy): writes through the grown slice must not alias
// the old array's structs.

func main() {
	a := []Rec{{"a", 1}}
	b := append(a, Rec{"b", 2})
	b[0].Name = "X"
	fmt.Println(a[0], b[0])

	c := slices.Insert(a, 0, Rec{"c", 0})
	c[1].Name = "Y"
	fmt.Println(a[0], c[1])

	d := slices.Grow(a, 4)
	d[0].Name = "Z"
	fmt.Println(a[0], d[0])

	e := slices.AppendSeq(a, slices.Values([]Rec{{"e", 9}}))
	e[0].Name = "W"
	fmt.Println(a[0], e[0])
}
