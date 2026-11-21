package main

import (
	"fmt"
	"time"
	"unsafe"
)

var z int = 1

func main() {
	// func-result operand: sizes the return's typedef (Duration = 8).
	var t time.Time
	fmt.Println(unsafe.Sizeof(time.Since(t)))
	// conversion operand: never evaluates (1/z would trap if run).
	fmt.Println(unsafe.Sizeof(time.Duration(1 / z)))
}
