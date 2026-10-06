package main

import "fmt"

func main() {
	m := map[int]int{1: 2, 2: 4, 4: 8, 8: 16}
	for k := range m {
		delete(m, k)
	}
	fmt.Println(len(m))
}
