package main

import (
	"fmt"
	"time"
)

func main() {
	c := make(chan int)
	go func() {
		time.Sleep(50 * time.Millisecond)
	}()
	fmt.Println("parked")
	<-c // the sleeper exits leaving only parked goroutines: gc aborts
}
