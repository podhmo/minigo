package main

import "fmt"

func seq2(yield func(string, int) bool) {
	yield("a", 1)
	yield("b", 2)
}
func seq2v(yield func(string, int) bool) {
	yield("a", 1)
}
func seq1(yield func(int) bool) {
	for i := 0; i < 3; i++ {
		if !yield(i * 10) {
			return
		}
	}
}
func seqN(yield func() bool) {
	yield()
	yield()
}
func seqEarly(yield func(int, int) bool) {
	calls := 0
	for i := 0; i < 5; i++ {
		calls++
		if !yield(i, i) {
			fmt.Println("producer saw stop at", calls)
			return
		}
	}
}

func main() {
	// one variable over a two-yield producer keeps the first — Go binds
	// the leading value and drops the rest.
	for k := range seq2 {
		fmt.Println("k", k)
	}
	// zero variables still drives the producer
	for range seq2v {
	}
	// matching arity unchanged
	for k, v := range seq2v {
		fmt.Println("kv", k, v)
	}
	for x := range seq1 {
		fmt.Println("x", x)
	}
	for range seqN {
		fmt.Println("n")
	}
	// under-yield would be a compile error in Go; here exercise the
	// one-var-over-two truncation again through a producer that also
	// observes the false return.
	for x := range seqEarly {
		fmt.Println("e", x)
		if x > 1 {
			break
		}
	}
}
