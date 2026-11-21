package main

import (
	"fmt"
	"time"
)

func sink(v bool, i int) bool { return v || i > 0 }

func main() {
	// both operands are undecidable member calls; the gate must still
	// catch the named-type mismatch once the members resolve.
	fmt.Println(sink(time.Duration(1) == time.Month(1), 0))
}
