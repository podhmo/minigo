package main

import "fmt"

type A [2]int
type B [2]int

type I interface{}

func main() {
	ch := make(chan int)
	var send chan<- int = ch
	var recv <-chan int = ch
	// static == resolves by assignability: the shared channel is equal.
	fmt.Println(send == ch, recv == ch)
	// interface == compares (dynamic type, value) pairs: chan<-int and
	// chan int are different types, so every pairing below is false.
	fmt.Println(any(send) == any(ch), any(recv) == any(ch))
	fmt.Println(any(send) == ch, send == any(ch))
	a, b := A{1, 2}, B{1, 2}
	fmt.Println(any(a) == any(b), any(a) == any([2]int{1, 2}))
	fmt.Println(a == A{1, 2})
	// named interface conversions behave like any.
	fmt.Println(I(a) == I(b), I(ch) == I(recv))
	fmt.Println(any(a) != any(b), any(ch) != any(recv))
	fmt.Println(any(1) == any(1.0), any(1) == any(1))
}
