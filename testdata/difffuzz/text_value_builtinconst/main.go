package main

import "fmt"

const N = 40
const S = "hello"
const A = 2
const B = 1

func main() {
	a := make([]uint8, N)
	fmt.Println(len(a), cap(a))
	ch := make(chan int, N)
	fmt.Println(cap(ch))
	fmt.Println(len(S))
	fmt.Println(min(A, B), max(A, B))
	fmt.Println(len(make([]int, N+1)))
}
