package main

import "fmt"

// A panic raised inside a deferred call supersedes the panic unwinding
// below: script-side, surviving defers' recover() hands out the
// SUPERSEDING panic's value; on the fatal path gc prints the chain
// `panic: <superseded> [recovered]` / `\tpanic: <new>` (the marker is
// stderr-only — the observable half is pinned here, the text is locked
// by TestPanicTraceback).

func f() {
	defer func() { fmt.Println("outer recover:", recover()) }()
	defer func() {
		fmt.Println("mid recover:", recover())
		panic("second")
	}()
	panic("first")
}

// two recovered-then-repanicked links in a row.
func g() {
	defer func() { fmt.Println("d1 recover:", recover()) }()
	defer func() { fmt.Println("d2 recover:", recover()); panic("second") }()
	defer func() { fmt.Println("d3 recover:", recover()); panic("first") }()
	panic("orig")
}

// a repanic WITHOUT recover supersedes an unrecovered panic: surviving
// defers still see the newest panic value.
func h() {
	defer func() { fmt.Println("h-inner recover:", recover()) }()
	defer func() { panic("h-second") }()
	panic("h-first")
}

func main() {
	f()
	g()
	h()
	fmt.Println("all returned")
}
