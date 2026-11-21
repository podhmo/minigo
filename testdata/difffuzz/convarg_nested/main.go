package main

import (
	"fmt"
	"time"
)

func wrap(d time.Duration) int { return int(d) }

func main() {
	// the inner member call must run bump once — not once for the
	// outer hoist and once inside it.
	n := 0
	bump := func() int { n++; return n }
	fmt.Println(wrap(time.Duration(bump())), n)
}
