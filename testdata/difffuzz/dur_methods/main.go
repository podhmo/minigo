package main

import (
	"fmt"
	"time"
)

func main() {
	var d time.Duration = 3 * time.Second
	fmt.Println(d.String(), d.Hours())

	// zero and literal binds keep the host scalar payload too —
	// their %v reads "0s"/"5ns", not the bare int64.
	var z time.Duration
	fmt.Println(z)

	// the host method set satisfies fmt.Stringer for interface binds.
	var s fmt.Stringer = d
	fmt.Println(s.String())
}
