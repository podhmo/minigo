package main

import (
	"fmt"
	"slices"
)

func main() {
	fmt.Println(cap(append(make([]byte, 3), make([]byte, 4)...)))
	s := make([]byte, 0, 3)
	s = append(s, 1, 2, 3, 4)
	fmt.Println(cap(s))
	var v0 []byte
	v0 = append(v0, 1, 2, 3, 4)
	fmt.Println(cap(v0))
	v0 = v0[:0]
	v0 = append(v0, make([]byte, 4)...)
	fmt.Println(cap(v0))
	i := make([]int, 0, 3)
	i = append(i, 1, 2, 3, 4)
	fmt.Println(cap(i))
	st := make([]string, 0, 3)
	st = append(st, "a", "b", "c", "d")
	fmt.Println(cap(st))
	type S16 struct{ a, b int64 }
	fmt.Println(cap(append(make([]S16, 3), make([]S16, 4)...)))
	a := make([]int, 5)
	fmt.Println(cap(slices.Grow(a, 1)))
	c := slices.Grow(make([]byte, 5), 3)
	fmt.Println(cap(c))
	var nb []byte
	fmt.Println(cap(slices.Grow(nb, 4)))
}
