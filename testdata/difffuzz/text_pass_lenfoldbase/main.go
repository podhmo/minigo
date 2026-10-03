package main

import "fmt"

func f() [][3]int { return nil }

func g() [1][3]int { return [1][3]int{} }

// len/cap fold must not drop calls inside the operand's base — Go
// folds the index only when nothing in the whole operand calls out.
func main() {
	defer func() { fmt.Println("recover:", recover() != nil) }()
	a := [1][3]int{}
	fmt.Println(len(a[0]))   // plain index still folds
	fmt.Println(cap(g()[0])) // call in base: evaluates, then folds to 3
	fmt.Println(len(f()[0])) // call in base: evaluates, then bounds panics
}
