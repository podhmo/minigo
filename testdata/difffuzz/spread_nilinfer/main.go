package main

import "fmt"

func Sum[T ~int | ~float64](xs ...T) T {
	var t T
	for _, x := range xs {
		t += x
	}
	return t
}

func main() {
	var n []int
	fmt.Println(Sum(n...))
}
