package main

import (
	"fmt"
	"time"
)

type D int

// a bound qualified const carries its typedef tag — `d == time.January`
// is a compile error (mismatched types D and time.Month).
func main() {
	var d D
	fmt.Println(d == time.January)
}
