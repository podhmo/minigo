package main

import "fmt"

func g[P any](args ...P) P {
	if len(args) == 0 {
		var zero P
		return zero
	}
	return args[0]
}

func main() {
	// untyped constant args unify to their common default type:
	// rune + float -> float64
	var x float64 = g('a', 2.3)
	fmt.Println(x)
}
