package main

import "fmt"

var a [1e1]int
var b ['a' - 'W']int
var c [1_0]int

func main() {
	fmt.Println(len(a), len(b), len(c))
}
