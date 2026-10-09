package main

import "fmt"

func main() {
	c := make(chan int)
	go func() { c <- 1 }()
	fmt.Println(<-c)
	<-c // every goroutine left is asleep: gc aborts with a deadlock fatal
}
