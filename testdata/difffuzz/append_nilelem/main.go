package main

import "fmt"

type List []int

func (l *List) Add(x int) { *l = append(*l, x) }

func main() {
	var s []List
	s = append(s, nil)
	fmt.Println(s) // go prints [[]] — a bare NIL element would print [<nil>]
	s[0].Add(1)
	fmt.Println(s) // a bare NIL element would panic on select instead
}
