package main

import (
	"context"
	"fmt"
)

// A host-returned nil channel (context.Background().Done) reaches
// channel ops as an untyped nil — a select arm on it never fires, like
// a typed nil channel, and it still reads `== nil`.

func main() {
	d := context.Background().Done()
	fmt.Println(d == nil)
	select {
	case <-d:
		fmt.Println("fired")
	default:
		fmt.Println("blocked")
	}
	ready := make(chan int, 1)
	ready <- 7
	select {
	case <-d:
		fmt.Println("fired")
	case v := <-ready:
		fmt.Println("ready", v)
	}
}
