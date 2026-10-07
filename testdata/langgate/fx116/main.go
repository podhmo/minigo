package main

// indexing through a field or method selection — p.anchors[n.Anchor],
// node.Content[i+1] — is plain indexing, never pkg.F[T] instantiation:
// go.yaml.in/yaml/v3 (go 1.16) is full of it.

type node struct {
	Anchor  string
	Content []int
}

type parser struct {
	anchors map[string]int
	tokens  []int
	head    int
}

func F() int {
	n := node{Anchor: "a", Content: []int{1, 2, 3}}
	p := parser{anchors: map[string]int{"a": 10}, tokens: []int{4, 5, 6}}
	i := 0
	return p.anchors[n.Anchor] + n.Content[i+1] + p.tokens[p.head+1]
}

func main() {}
