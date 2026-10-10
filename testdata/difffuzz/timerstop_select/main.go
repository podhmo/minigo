package main

import (
	"fmt"
	"time"
)

// A select whose only arm is a stopped timer's channel has no wake
// source left: parking there is a deadlock, not a hang.
func main() {
	t := time.NewTimer(30 * time.Second)
	t.Stop()
	fmt.Println("about to select")
	select {
	case <-t.C:
		fmt.Println("unreachable")
	}
}
