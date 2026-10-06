package main

import "fmt"

// Reslicing a virtual zero-size slice must run Go's bounds checks —
// the elements were never materialized but the limits still bind.

var big = 1 << 34

func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("panicked")
		}
	}()
	s := make([]struct{}, 1<<33)
	fmt.Println(len(s[:big]))
	fmt.Println("unreached")
}
