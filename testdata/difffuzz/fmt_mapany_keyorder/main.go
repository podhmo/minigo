package main

import "fmt"

type N int
type S string

func main() {
	fmt.Println(map[any]string{"s": "s", 1: "i"})
	fmt.Println(map[any]string{true: "b", 1.5: "f", "a": "s", 2: "i", uint(3): "u"})
	fmt.Println(map[any]int{int8(2): 1, uint8(7): 2, int32(4): 3, int64(8): 4, int(1): 5, "x": 6})
	fmt.Println(map[any]int{N(9): 1, S("s"): 2, "a": 3, 1: 4})
	fmt.Println(map[any]int{complex128(1 + 2i): 1, float32(1.5): 2, uintptr(2): 3})
}
