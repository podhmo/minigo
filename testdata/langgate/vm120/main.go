package main

// A local var is in scope after its spec, so the initializer min is
// the go1.21 builtin.
func M() int {
	var min = min(1, 2)
	return min
}

func main() {}
