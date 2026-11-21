package main

import (
	"fmt"
	"strconv"
	"time"
)

// A declared variable's type tags the arg: gc rejects `cannot use d
// (variable of int64 type time.Duration) as int64 value` — the named
// int64 underneath is not the parameter's plain int64.
func main() {
	var d time.Duration = 3
	fmt.Println("start")
	fmt.Println(strconv.FormatInt(d, 10))
}
