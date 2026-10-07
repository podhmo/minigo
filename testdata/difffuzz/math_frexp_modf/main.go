package main

import (
	"fmt"
	"math"
)

func main() {
	f, e := math.Frexp(40)
	fmt.Println(f, e)
	i, fr := math.Modf(-3.25)
	fmt.Println(i, fr)
	f, e = math.Frexp(0)
	fmt.Println(f, e, math.Ldexp(0.625, 6))
}
