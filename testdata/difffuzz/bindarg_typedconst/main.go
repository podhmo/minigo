package main

import (
	"fmt"
	"strconv"
)

// gc rejects a typed constant crossing a bound call: `cannot use c
// (constant 5 of type int8) as int value in argument to strconv.Itoa`.
func main() {
	const c int8 = 5
	fmt.Println("start")
	fmt.Println(strconv.Itoa(c))
}
