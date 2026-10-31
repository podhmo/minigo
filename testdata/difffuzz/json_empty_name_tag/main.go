package main

import (
	"encoding/json"
	"fmt"
)

type Node struct {
	V    int     `json:",omitempty"`
	Next *Node   `json:",omitempty"`
	S    []int   `json:",omitempty"`
	N    int     `json:",string"`
	F    float64 `json:",omitempty,string"`
	E    string  `json:""`
	Keep int     `json:"keep,omitempty"`
}

type Outer struct {
	Node `json:",omitempty"`
	X    int
}

func main() {
	for _, v := range []any{Node{}, Node{V: 1, N: 2, F: 1.5, Next: &Node{V: 2}}, Outer{X: 1}} {
		b, err := json.Marshal(v)
		fmt.Println(string(b), err)
	}
	var n Node
	err := json.Unmarshal([]byte(`{"V":3,"N":"4","F":"2.5","E":"e"}`), &n)
	fmt.Println(n.V, n.N, n.F, n.E, err)
}
