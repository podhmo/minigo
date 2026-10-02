package main

import "fmt"

var ch = make(chan int, 1)

func idx() int {
	fmt.Println("idx called")
	return 0
}

func main() {
	ch <- 0
	var a [1][3]int
	// the index contains a call / receive, so Go evaluates it even
	// though the element type would let len/cap fold to a constant.
	fmt.Println(len(a[idx()]))
	fmt.Println(cap(a[<-ch]))
	// a constant-call-free index still folds.
	fmt.Println(len(a[0]))
}
