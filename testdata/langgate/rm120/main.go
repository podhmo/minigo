package main

// A range binding is in scope only over the loop body, so the range
// expression min is the go1.21 builtin.
func M() int {
	n := 0
	for min := range []int{min(1, 2)} {
		n += min
	}
	return n
}

func main() {}
