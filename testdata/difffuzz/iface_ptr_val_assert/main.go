package main

import "fmt"

type rd struct{ X int }

func main() {
	v := rd{3}
	var a any = v
	r, ok := a.(rd)
	fmt.Println("val:", ok, r)
	var b any = &v
	q, ok2 := b.(rd)
	fmt.Println("ptr:", ok2, q)
	p, ok3 := b.(*rd)
	fmt.Println("star:", ok3, p)
}
