package main

import (
	"fmt"
	"time"
)

// time.After's channel carries exactly one send: a second receive on the
// drained channel has no sender left and is a deadlock, not a hang.
func main() {
	ch := time.After(10 * time.Millisecond)
	<-ch
	fmt.Println("first receive done")
	<-ch
	fmt.Println("unreachable")
}
