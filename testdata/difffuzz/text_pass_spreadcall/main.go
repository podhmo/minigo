package main

import "fmt"

func swap(a, b int) (int, int) { return b, a }

func main() {
	fmt.Println(swap(swap(1, 2)))
}
