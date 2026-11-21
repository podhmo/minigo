package main

import (
	"fmt"
	"sync"
)

func main() {
	var m sync.Map
	m.Store(5, "x")
	v, ok := m.Load(int(5))
	fmt.Println(v, ok)
	fmt.Println(int(5) / 2.0)
	fmt.Println(any(int(5)) == any(5), any(uint16(8)) == any(int(8)))
	fmt.Println(float64(5)/2, string(65), bool(true) == true)
	fmt.Println(any(rune('a')) == any('a'), any(byte(97)) == any('a'))
	var mm map[int]int = map[int]int{}
	mm[int(5)] = 1
	fmt.Println(mm[5])
	fmt.Printf("%T\n", int(5))
}
