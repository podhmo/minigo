package main

import "fmt"

func main() {
	x := []int{1, 2, 3}
	i := 0
	x[i], i = 100, 1
	fmt.Println(x, i)

	m := map[int]int{}
	j := 0
	m[j], j = 7, 5
	fmt.Println(m, j)

	a, b := 1, 2
	a, b = b, a
	fmt.Println(a, b)

	s := []int{1, 2, 3}
	s[0], i = 9, 2
	fmt.Println(s, i)
}
