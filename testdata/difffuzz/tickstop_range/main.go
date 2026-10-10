package main

import (
	"fmt"
	"time"
)

// A stopped ticker never sends or closes its channel: ranging over it
// parks forever, which the runtime must report as a deadlock.
func main() {
	tk := time.NewTicker(30 * time.Second)
	tk.Stop()
	fmt.Println("about to range")
	for range tk.C {
		fmt.Println("tick")
	}
	fmt.Println("unreachable")
}
