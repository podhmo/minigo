package main

import (
	"fmt"
	"time"
)

func main() {
	c := make(chan int)
	t := time.AfterFunc(time.Hour, func() { c <- 1 })
	t.Stop()
	fmt.Println("parked")
	<-c // the stopped timer wakes nobody: gc aborts with a deadlock fatal
}
