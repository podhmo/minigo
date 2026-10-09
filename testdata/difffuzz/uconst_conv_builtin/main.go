package main

// A converted constant T(c) crossing a bound host call materializes at
// T, not the int/int64 default: uint64(1<<63) reaches fmt as a uint64
// (the default would overflow int64 and panic inside Format).

import "fmt"

func main() {
	fmt.Println(fmt.Sprintf("%x", uint64(1<<63)))
	fmt.Println(fmt.Sprintf("%v", uint(1<<63)))
	fmt.Println(fmt.Sprintf("%d", int64(1<<62)))
	fmt.Println(fmt.Sprintf("%g", float64(1<<40)))
}
