package main

import (
	"fmt"
	"time"
)

// A receive on a stopped timer's channel can never complete: with every
// goroutine parked the runtime must abort, not hang.
func main() {
	t := time.NewTimer(30 * time.Second)
	t.Stop()
	fmt.Println("about to receive")
	<-t.C
	fmt.Println("unreachable")
}
