package main

// COMPILE_TIME_FACT: an inner type shadow never retypes an outer var

import "fmt"

func main() {
	var x int = 3
	for i := range []int{0} {
		// shadows int only inside the loop body — x was declared
		// outside and stays builtin int
		type int uint8
		fmt.Println(x == i+3)
	}
}
