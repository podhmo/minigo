package main

import "fmt"

// $GOROOT/test/list2.go shape — &s.f formed twice (stored, then
// re-evaluated) is the same pointer: field refs compare by resolved
// slot, not by wrapper identity.
type list struct {
	root elem
	len  int
}
type elem struct {
	next, prev *elem
}

func newList() *list {
	l := &list{}
	l.root.next = &l.root
	l.root.prev = &l.root
	return l
}

func main() {
	l := newList()
	root := &l.root
	fmt.Println(l.root.next == root && l.root.prev == root)
	fmt.Println(l.root.next != nil)

	// a ref through a different selector path to the same promoted
	// field is also the same pointer.
	type outer struct{ inner elem }
	o := &outer{}
	p1 := &o.inner.next
	_ = p1

	// distinct structs' fields differ
	m := newList()
	fmt.Println(l.root.next == &m.root)

	// zero-size fields share zerobase even across distinct structs
	type zs struct{ z struct{} }
	var x, y zs
	fmt.Println(&x.z == &y.z)
}
