package main

import (
	"fmt"
	"time"
)

func main() {
	// a raw host integer scalar (time.Duration keeps its host payload)
	// reads as int64 wherever a numeric operand is consumed — index,
	// slice bounds, min/max, shift count, bitwise complement.
	var d time.Duration = 1
	var d2 time.Duration = 2
	a := []int{7, 8}
	fmt.Println(a[d])
	fmt.Println(a[d:])
	fmt.Println(min(d, d2), max(d, d2))
	fmt.Println(1 << d2)
	fmt.Println(^d2)
	arr := [4]int{9, 8, 7, 6}
	fmt.Println(arr[d:])
}
