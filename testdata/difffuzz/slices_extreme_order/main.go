package main

import (
	"fmt"
	"math"
	"slices"
)

func main() {
	nan := math.NaN()
	pz, nz := 0.0, math.Copysign(0, -1)

	// gc propagates NaN through both extremes.
	fmt.Println(slices.Max([]float64{1, 2, nan}))
	fmt.Println(slices.Min([]float64{nan, 1, 2}))
	fmt.Println(slices.Min([]float64{1, nan, 2}))

	// Max prefers +0, Min prefers -0, either order.
	fmt.Println(math.Signbit(slices.Max([]float64{nz, pz})), math.Signbit(slices.Max([]float64{pz, nz})))
	fmt.Println(math.Signbit(slices.Min([]float64{pz, nz})), math.Signbit(slices.Min([]float64{nz, pz})))

	// uint64 above MaxInt64 orders exactly, sorted and as an extreme.
	us := []uint64{math.MaxUint64, math.MaxInt64, 3}
	slices.Sort(us)
	fmt.Println(us, slices.IsSorted(us))
	fmt.Println(slices.Max(us), slices.Min(us))

	// int64 neighbours past 2^53 stay distinct.
	fmt.Println(slices.Max([]int64{math.MaxInt64 - 1, math.MaxInt64}))

	// NaN still orders first in a sort; ±0 keep cmp.Compare order.
	fs := []float64{1, nan, -3, nz, pz}
	slices.Sort(fs)
	fmt.Println(fs)
	fmt.Println(slices.Compare([]float64{nz}, []float64{pz}))
}
