package main

import (
	"fmt"
	"math"
)

func main() {
	s, c := math.Sincos(0)
	fmt.Println(s, c)
	s2, c2 := math.Sincos(math.Pi / 4)
	fmt.Println(s2, c2)
}
