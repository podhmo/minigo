package main

import (
	"fmt"
	"time"
)

func main() {
	// package consts vs converted values: same type, same value.
	m := time.January
	fmt.Println(m == time.Month(1))
	fmt.Println(time.Month(1) == m)
	fmt.Println(m == 1, 1 == m)
	var x = 1
	fmt.Println(time.Month(x) == time.January)
	var d time.Month = time.December
	fmt.Println(d == time.Month(12), d == time.December)
	fmt.Println(time.January < time.February, time.June < time.May)
	fmt.Println(int(time.January), int(time.Month(8)))
	fmt.Printf("%T\n", m)
	switch m {
	case time.January:
		fmt.Println("jan")
	default:
		fmt.Println("other")
	}
}
