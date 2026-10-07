package main

import "fmt"

func main() {
	ch1 := make(chan struct{})
	var recv <-chan struct{} = ch1
	var send chan<- struct{} = ch1
	fmt.Println(ch1 == recv)
	fmt.Println(recv == ch1)
	fmt.Println(ch1 == send)
	fmt.Println(ch1 == make(chan struct{}))
}
