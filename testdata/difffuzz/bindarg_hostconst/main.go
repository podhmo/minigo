package main

import (
	"fmt"
	"strconv"
	"time"
)

// A bound package's typed constant carries its host type across the
// call — gc rejects `cannot use time.Second (constant 1000000000 of
// type time.Duration) as int64 value`.
func main() {
	fmt.Println("start")
	fmt.Println(strconv.FormatInt(time.Second, 10))
}
