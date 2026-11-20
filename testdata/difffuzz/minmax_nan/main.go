package main

import (
	"fmt"
	"math"
)

type F float64

func main() {
	fmt.Println(max(1.0, math.NaN()))
	fmt.Println(min(1.0, math.NaN()))
	fmt.Println(max(math.NaN(), 1.0))
	fmt.Println(min(math.NaN(), 1.0))
	fmt.Printf("%T %v\n", max(float32(1), float32(math.NaN())), max(float32(1), float32(math.NaN())))
	fmt.Println(min(F(1), F(math.NaN())))
	fmt.Println(max(3, 1))
	fmt.Println(min(3, 1))
}
