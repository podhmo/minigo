package main

import "fmt"

func main() {
	var i int16 = -1
	fmt.Println(uint(i))
	var u uint = 0
	fmt.Println(^u)
	var u2 uint = 2
	fmt.Println(-u2)
	fmt.Println(float64(u2 - 100))
	var w uint = 18446744073709551615
	fmt.Printf("%T %v\n", w, w)
	fmt.Println(float64(w))
	var w64 uint64 = 18446744073709551615
	fmt.Println(float64(w64))
	var p uintptr = 0
	fmt.Println(^p)
	unary()
}

func unary() {
	var u uint = 18446744073709551615
	fmt.Printf("%T %v\n", -u, -u)
	var u2 uint = 2
	fmt.Printf("%T %v\n", -u2, -u2)
	fmt.Printf("%T %v\n", ^u, ^u)
}
