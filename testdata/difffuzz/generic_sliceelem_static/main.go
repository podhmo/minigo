// A slice argument binds a generic element parameter to the slice's
// declared element type, not to its first element's dynamic type —
// found via go/ast.walkList[N Node](v, list []N) (ast.Inspect/ast.Walk)
// while running go/parser on grafana sources (realworld grafana-openapi).
package main

import "fmt"

type Node interface{ Pos() int }

type Stmt interface {
	Node
	stmtNode()
}

type A struct{ n int }

func (a *A) Pos() int  { return a.n }
func (a *A) stmtNode() {}

func walkList[N Node](list []N) string {
	t := 0
	for _, n := range list {
		t += n.Pos()
	}
	return fmt.Sprintf("%T sum=%d", list, t)
}

func ptrs[T any](xs []*T) int { return len(xs) }

func mk() []*A {
	var out []*A
	for i := 1; i <= 3; i++ {
		out = append(out, &A{i})
	}
	return out
}

type holder struct {
	stmts []Stmt
	nodes []Node
}

func main() {
	var ss []Stmt = []Stmt{&A{1}, &A{2}}
	fmt.Println(walkList(ss))
	fmt.Println(walkList([]Node{&A{5}}))
	fmt.Println(walkList([]Stmt{&A{6}}))
	fmt.Println(walkList([]*A{{3}, {4}}))
	fmt.Println(walkList(mk()[1:]))
	var empty []Stmt
	fmt.Println(walkList(empty))
	fmt.Println(ptrs(mk()))

	h := holder{stmts: ss, nodes: []Node{&A{7}, &A{8}}}
	fmt.Println(walkList(h.stmts))
	fmt.Println(walkList(h.nodes))
	m := map[string][]Node{"x": {&A{9}}}
	fmt.Println(walkList(m["x"]))
}
