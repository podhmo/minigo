package main

import "fmt"

// A virtual zero-size slice must keep its logical capacity through
// s[:0]: the reslice is still backed by the huge virtual array, so a
// legal grow-back works.

func main() {
	s := make([]struct{}, 1<<33)
	t := s[:0]
	fmt.Println(len(t), cap(t))
	fmt.Println(len(t[:1]))
}
