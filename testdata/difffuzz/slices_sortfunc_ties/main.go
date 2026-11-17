package main

import (
	"fmt"
	"math/rand"
	"slices"
)

type rec struct {
	k int
	i int
}

func main() {
	var xs []rec
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 100; i++ {
		xs = append(xs, rec{k: r.Intn(10), i: i})
	}
	slices.SortFunc(xs, func(a, b rec) int { return a.k - b.k })
	for _, x := range xs {
		fmt.Printf("%d/%d ", x.k, x.i)
	}
	fmt.Println()

	var xs2 []rec
	r2 := rand.New(rand.NewSource(42))
	for i := 0; i < 100; i++ {
		xs2 = append(xs2, rec{k: r2.Intn(10), i: i})
	}
	slices.SortStableFunc(xs2, func(a, b rec) int { return a.k - b.k })
	for _, x := range xs2 {
		fmt.Printf("%d/%d ", x.k, x.i)
	}
	fmt.Println()
}
