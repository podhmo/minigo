package main

import "fmt"

type MyU8 uint8

func main() {
	var i int = -3
	fmt.Println(uint16(i))
	var x int32 = -1
	fmt.Println(uint8(x))
	var u uintptr = 0xffffffffffffffff
	fmt.Println(uint32(u))
	fmt.Println(MyU8(x))
	var i64 int64 = -1
	fmt.Println(uint8(uint32(i64)))
}
