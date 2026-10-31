package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type Node struct {
	V    int
	Next *Node
}

type A struct{ B *B }
type B struct{ A *A }

type Tree map[string]any

type W struct{ M map[string]any }

func show(label string, v any) {
	_, err := json.Marshal(v)
	var b bytes.Buffer
	eerr := json.NewEncoder(&b).Encode(v)
	fmt.Printf("%s: %v | %v\n", label, err, eerr)
}

func main() {
	n := &Node{V: 1}
	n.Next = n
	show("self-ptr", n)

	a := &A{}
	a.B = &B{A: a}
	show("two-ptr", a)

	m := map[string]any{}
	m["self"] = m
	show("map", m)

	t := Tree{}
	t["self"] = t
	show("named-map", t)

	s := []any{nil}
	s[0] = s
	show("slice", s)

	w := &W{M: map[string]any{}}
	w.M["w"] = w
	show("struct-in-map", w)

	shared := &Node{V: 2}
	show("shared-no-cycle", []*Node{shared, shared})
}
