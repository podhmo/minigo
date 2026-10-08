package main

import "fmt"

// int→string must yield U+FFFD for values outside the rune range;
// truncating to rune first loses the range and emits a wrong rune
// (corpus fixedbugs/issue15039).
func main() {
	u := uint64(0x10001f4a9)
	fmt.Println(string(u))
	const huge = string(1 << 100)
	fmt.Println(huge)
}
