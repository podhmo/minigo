package main

// The scope of a := name starts after the statement, so the RHS min is
// still the go1.21 builtin.
func M() int {
	min := min(1, 2)
	return min
}

func main() {}
