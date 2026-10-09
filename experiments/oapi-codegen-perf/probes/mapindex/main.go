package main

import "fmt"

type I int
type S struct{ N int }

func main() {
	s := []S{{N: 1}}
	i := I(0)
	s[i].N++
	p := &s[i]
	fmt.Println(s[0].N, p == &s[0])
	m := map[I][]int{i: {1}}
	m[i][0]++
	fmt.Println(m[i][0])
}
