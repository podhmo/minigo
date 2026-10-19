package main

import (
	"fmt"
	"time"
)

// A bound package member may carry the raw host scalar — `time.Second`
// binds a bare time.Duration, not a Named — so the member's declared
// type comes from matching the host type back to the package's own
// typedefs. gc rejects `d == i`.
func main() {
	var d = time.Second
	var i int64
	fmt.Println(d == i)
}
