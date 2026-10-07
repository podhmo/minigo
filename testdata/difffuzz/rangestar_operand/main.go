package main

import "fmt"

// `range *e` iterates e's pointer lazily only when e is a pure storage
// shape — an operand that forces evaluation (a call or a channel
// receive anywhere inside) makes gc evaluate `*e` eagerly, so a nil
// pointer dereferences at range setup instead of yielding indices.

func get() *struct{ P *[3]int } { return &struct{ P *[3]int }{} }

func try(name string, f func()) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println(name, "panic")
		}
	}()
	f()
	fmt.Println(name, "ok")
}

func main() {
	try("callsel", func() { for i := range *get().P { _ = i } })
	ch := make(chan *[3]int, 1)
	ch <- nil
	try("recv", func() { for i := range *(<-ch) { _ = i } })
	m := map[int]*[3]int{}
	out := []int{}
	for i := range *m[0] { // map-index stays lazy — nil yields indices
		out = append(out, i)
	}
	fmt.Println("mapidx", out)
	try("mapidx-elem", func() { for i, v := range *m[0] { _, _ = i, v } })
	a := [3]*[3]int{}
	out = out[:0]
	for i := range *a[0] {
		out = append(out, i)
	}
	fmt.Println("arridx", out)
}
