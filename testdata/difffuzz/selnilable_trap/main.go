package main

import (
	"fmt"
	"time"
)

// a qualified named type that resolves non-nilable rejects `== nil`
// like gc's compile error — interface types keep the dynamic compare.
func main() {
	var d time.Duration = 3
	fmt.Println(d == nil)
}
