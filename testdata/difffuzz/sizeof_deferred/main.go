package main

import (
	"fmt"
	"time"
	"unsafe"
)

var z int

func main() {
	fmt.Println(unsafe.Sizeof(time.Duration(1 / z)))
}
