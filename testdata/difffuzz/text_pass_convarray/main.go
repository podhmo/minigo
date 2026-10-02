package main

import "fmt"

func main() {
	s := []int{1, 2, 3}
	p := (*[3]int)(s)
	fmt.Println(p[0], p[1], p[2])
	a := [2]int(s[:2])
	fmt.Println(a[0], a[1])
	var n []int
	z := [0]int(n)
	_ = z
	defer func() { fmt.Println(recover() != nil) }()
	_ = (*[2]int)(n)
}
