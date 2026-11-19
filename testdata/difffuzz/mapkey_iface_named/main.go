package main

import "fmt"

type C chan int
type St struct{ X int }
type NS St
type A [1]int

func main() {
	ch := make(chan int)
	m := map[any]int{ch: 1}
	fmt.Println(m[C(ch)], m[any(ch)])
	m[C(ch)] = 2
	fmt.Println(m[ch], len(m))

	st := St{X: 1}
	n := map[any]int{st: 10}
	fmt.Println(n[NS(st)], n[any(st)])
	n[NS(st)] = 20
	fmt.Println(n[st], len(n))

	a := [1]int{1}
	p := map[any]int{a: 5}
	fmt.Println(p[A(a)], p[any(a)])
	p[A(a)] = 6
	fmt.Println(p[a], len(p))
	declaredForm()
}

func declaredForm() {
	ch := make(chan int)
	m := map[any]int{ch: 1}
	m[C(ch)] = 2
	mc := make(C)
	fmt.Println(m[mc], len(m))
	m[C(mc)] = 9
	fmt.Println(m[mc], len(m))

	a := [1]int{1}
	p := map[any]int{a: 5}
	p[A(a)] = 6
	var aa A = [1]int{1}
	fmt.Println(p[aa], len(p))
}
