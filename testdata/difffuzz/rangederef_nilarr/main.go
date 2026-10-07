package main

import "fmt"

func nilPtrFunc() *[4]int { return nil }

// `range *p` on a nil *[N]T iterates the static indices 0..N-1 without
// dereferencing the pointer — the element read alone would panic.
// gc keeps that laziness only for lvalue-capable operands (variables,
// selectors, indexes, nested derefs); a call result evaluates `*x`
// eagerly and panics.
func main() {
	var p *[4]int
	for i := range *p {
		fmt.Println("var", i)
	}
	for i := range (*p) {
		fmt.Println("paren", i)
	}
	s := struct{ q *[4]int }{}
	for i := range *s.q {
		fmt.Println("sel", i)
	}
	func() {
		defer func() { fmt.Println("call:", recover()) }()
		for i := range *nilPtrFunc() {
			fmt.Println("call", i)
		}
	}()
	np := &[4]int{1, 2, 3, 4}
	for i, v := range *np {
		fmt.Println("live", i, v)
	}
	func() {
		defer func() { fmt.Println("elem:", recover()) }()
		for _, v := range *p {
			fmt.Println("elem", v)
		}
	}()
}
