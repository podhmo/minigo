package main

import "fmt"

func sum(xs ...int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

// a `xs...` spread must be the only argument past the callee's fixed
// params — gc rejects extras in front of it at compile time.
func main() {
	fmt.Println(sum(5, []int{1, 2}...))
}
