package main

import (
	"fmt"
	"maps"
)

// map[string]E over map[string]*Item infers E=*Item, not Item (and a
// pointer operand in a func type unifies as a pointer) —
// kin-openapi's componentNames(doc.Paths.Map()).

type Item struct{ N int }

type Paths struct{ m map[string]*Item }

func (p *Paths) Map() (m map[string]*Item) {
	m = make(map[string]*Item, len(p.m))
	maps.Copy(m, p.m)
	return
}

func names[E any](s map[string]E) string {
	var z E
	return fmt.Sprintf("%d %T", len(s), z)
}

func kv[K comparable, V any](m map[K]V) string {
	var k K
	var v V
	return fmt.Sprintf("%T %T", k, v)
}

// the same pointer operand inside a func type's params
func apply[T any](f func(*T) int) string {
	var z T
	return fmt.Sprintf("%T", z)
}

func ret[T any](f func() T) string {
	var z T
	return fmt.Sprintf("%T", z)
}

func main() {
	fmt.Println(apply(func(i *Item) int { return 0 }), ret(func() *Item { return nil }))
	p := &Paths{m: map[string]*Item{"/a": {1}}}
	fmt.Println(names(map[string]*Item{"x": {2}}))
	fmt.Println(names(p.Map()))
	fmt.Println(names(map[string][]*Item{}))
	fmt.Println(kv(map[*Item]*Item{}), kv(map[string]map[int]*Item{}))
}
