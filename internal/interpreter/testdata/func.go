package main

import "fmt"

func main() {
	fmt.Println(F(10))
	H(100)

	// Output:
	// 50
	// H 100
}

func F(n int) int {
	return G(n) + G(n) + 10
}

func G(n int) int {
	return n + 10
}

func H(n int) {
	fmt.Println("H", n)
	return
}
