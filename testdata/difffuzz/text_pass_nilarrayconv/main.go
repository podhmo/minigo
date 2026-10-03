package main

import "fmt"

func main() {
	var s []int
	// nil slice → [0]T succeeds (length 0 is enough).
	var a [0]int = [0]int(s)
	fmt.Println(len(a))
	defer func() { fmt.Println("recovered:", recover() != nil) }()
	// ...but [1]T needs length >= 1 — Go panics at run time.
	_ = [1]int(s)
}
