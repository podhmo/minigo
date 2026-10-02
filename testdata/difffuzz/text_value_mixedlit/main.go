package main

import "fmt"

var b4 = &[16]byte{4: 1, 1, 1, 1, 12: 1, 1}
var b5 = &[16]byte{1, 4: 1, 1, 1, 1, 12: 1, 1}
var b6 = &[...]byte{1, 4: 1, 1, 1, 1, 12: 1, 1, 0, 0}
var s1 = []int{2: 9, 8, 7}
var s2 = []int{3, 4: 8}

func main() {
	fmt.Println(*b4)
	fmt.Println(*b5)
	fmt.Println(*b6)
	fmt.Println(s1)
	fmt.Println(s2)
}
