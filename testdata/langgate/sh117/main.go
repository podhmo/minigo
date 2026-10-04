package main

// the package declares its own `any`, `min` and `new`: predeclared-name
// gates must not fire — the old identifiers keep their local meaning.

type any = int

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var new = func(v int) *int { return &v }

var X any = 1

func M() int { return min(1, 2) }

func N() *int { return new(42) }

func main() {}
