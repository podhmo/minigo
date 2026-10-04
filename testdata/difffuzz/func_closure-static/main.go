package main

import "fmt"

var counter int

func bump() func() int { return func() int { counter++; return counter } }

func main() {
	// capture-free literals: repeated evals share the proto
	f1, f2 := bump(), bump()
	fmt.Println(f1(), f2())

	add := func(x int) func(int) int { return func(y int) int { return x + y } }
	a3, a5 := add(3), add(5)
	fmt.Println(a3(1), a5(1), a3(10))

	var fs []func() int
	for i := 0; i < 3; i++ {
		i := i
		fs = append(fs, func() int { return i * 10 })
	}
	for _, f := range fs {
		fmt.Print(f(), " ")
	}
	fmt.Println()
}
