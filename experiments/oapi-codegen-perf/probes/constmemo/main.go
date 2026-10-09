package main

import "fmt"

type N int

func f(x N) N { return 1 + x }
func main() {
	for i := 0; i < 3; i++ {
		a := f(N(i))
		a++
		fmt.Println(a, f(N(i)))
	}
}
