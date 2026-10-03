package main

import "fmt"

func main() {
	fmt.Printf("%v %[2]v %v\n", "a", "b", 4, 5, 6)
	fmt.Printf("%*v\n", 8, "x", 9)
}
