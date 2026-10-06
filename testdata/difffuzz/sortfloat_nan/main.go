package main

import (
	"fmt"
	"math"
	"sort"
)

func main() {
	s := []float64{2.0, math.NaN(), -1.0}
	sort.Float64s(s)
	fmt.Println(s)
}
