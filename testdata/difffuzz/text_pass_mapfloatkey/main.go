package main

import (
	"fmt"
	"math"
)

type M map[int]int

func main() {
	// keyed literals on a named map type evaluate keys as expressions
	m := M{}
	for i := 0; i < 3; i++ {
		m2 := M{i: i + 1}
		m[i] = m2[i]
	}
	for i := 0; i < 3; i++ {
		fmt.Printf("m[%d]=%v\n", i, m[i])
	}

	// float map keys: -0 == +0, NaN never matches
	pz := float32(0)
	nz := math.Float32frombits(1 << 31)
	fmt.Println(nz == pz)
	fm := map[float32]string{pz: "+0", math.Float32frombits(0x7fc00000): "nan", math.Float32frombits(0x7fc00002): "nan2"}
	fm[nz] = "-0"
	fmt.Println(fm[pz], len(fm))
	_, ok := fm[math.Float32frombits(0x7fc00003)]
	fmt.Println(ok)

	// NaN keys iterate but never read back
	n := map[float64]int{}
	nan := math.NaN()
	for i := 0; i < 3; i++ {
		n[nan] = i
	}
	fmt.Println(len(n))
	iters, sum := 0, 0
	for k, v := range n {
		iters++
		if math.IsNaN(k) {
			sum += v
		}
	}
	fmt.Println(iters, sum, n[nan] == 0)
}
