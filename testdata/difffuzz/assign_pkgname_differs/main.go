package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/assign_pkgname_differs/inner.v2"
)

func clone(n *inner.Node) *inner.Node {
	out := &inner.Node{Kind: n.Kind}
	if n.Content != nil {
		out.Content = make([]*inner.Node, len(n.Content))
		for i, c := range n.Content {
			out.Content[i] = clone(c)
		}
	}
	out.Index = map[string]*inner.Node{}
	var kids []*inner.Node = out.Content
	out.Content = append(kids, &inner.Node{Kind: 9})
	return out
}

func main() {
	n := &inner.Node{Kind: 1, Content: []*inner.Node{{Kind: 2}}}
	c := clone(n)
	fmt.Println(c.Kind, len(c.Content), c.Content[0].Kind, c.Content[1].Kind, c.Content[0] != n.Content[0])
	fmt.Printf("%T %T %v\n", c.Content, c.Index, any(c.Content).([]*inner.Node) != nil)

	// assigning through a package qualifier writes the package global —
	// the clause name (inner) is the qualifier, not the path tail
	// (inner.v2), so the compile side must resolve it like the VM does.
	inner.Count = 41
	inner.Shared = &inner.Node{Kind: 9}
	fmt.Println(inner.Count, inner.Shared.Kind)
}
