package main

import (
	"fmt"
	"math"
)

// host uint64 results were boxed as *GoValue and never compared equal
// to constants — math.Float64bits answers made every != fire.
func main() {
	fmt.Println(math.Float64bits(1.5) == 0x3FF8000000000000)
	fmt.Println(math.Float64bits(-2.0) == 0xC000000000000000)
	fmt.Println(math.Float64bits(0.5)+1 == 0x3FE0000000000001)
	// wide values (>= 2^63) still box — equality must hold too
	v := 0.0
	fmt.Println(math.Float64bits(-v) == 0x8000000000000000)
}
