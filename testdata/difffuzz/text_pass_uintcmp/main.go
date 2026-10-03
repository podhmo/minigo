package main

import "fmt"

func main() {
	// `u != 0` on a named/boxed unsigned routed into uintBinOp, which
	// had no Eql/Neq cases and trapped "uint binary !=".
	var u uint = 3
	fmt.Println(u != 0, u == 3, u != 3)
	var u8 uint8 = 255
	fmt.Println(u8 == 255, u8 != 255)
	u64 := uint64(1<<63 - 1)
	fmt.Println(u64 != 0, u64 == u64)
	var up uintptr = 16
	fmt.Println(up != 16, up == 16)
}
