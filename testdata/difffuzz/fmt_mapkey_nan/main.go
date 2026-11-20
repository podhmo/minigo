package main

import (
	"fmt"
	"math"
)

func main() {
	m := map[float64]int{math.NaN(): 1, 1: 3}
	fmt.Println(m)
	m2 := map[float64]int{math.NaN(): 1, math.NaN(): 2, 1: 3}
	fmt.Println(m2)
}
