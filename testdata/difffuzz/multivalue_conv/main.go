package main

import "fmt"

type A any

func g() (int, bool) { return 1, true }

func sink(v ...any) { fmt.Println(v...) }

func main() {
	// a conversion is a single-value context too — gc rejects
	// `A(g())` with "multiple-value ... in single-value context";
	// the lone-arg spread arm must not let the tuple in.
	sink("x", A(g()))
}
