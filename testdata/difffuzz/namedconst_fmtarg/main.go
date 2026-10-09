package main

import "fmt"

func main() {
	// a still-constant argument materializes at its declared width —
	// the untyped default would overflow int.
	fmt.Println(uint64(18446744073709551615))
	fmt.Println(int64(-9223372036854775808))
	fmt.Println(byte(255))
	fmt.Println(float32(1.5))
	fmt.Println(1685)
}
