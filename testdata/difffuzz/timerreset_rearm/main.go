package main

import (
	"fmt"
	"time"
)

// Reset on a stopped timer re-arms its channel: a parked receive must
// count wakeable again and see the fire, like gc reschedules the send.
func main() {
	t := time.NewTimer(30 * time.Second)
	t.Stop()
	t.Reset(20 * time.Millisecond)
	select {
	case <-t.C:
		fmt.Println("reset fired")
	case <-time.After(2 * time.Second):
		fmt.Println("no fire")
	}
}
