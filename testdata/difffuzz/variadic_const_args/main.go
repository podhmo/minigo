package main

import "fmt"

// A variadic call's trailing arguments are assigned to the element type
// one by one: untyped constants convert to it (`f(1)` for `...float64`
// holds 1.0, so x/2 is 0.5), a non-constant shift `1<<n` truncates to
// uint8, and `...any` boxes each at its default type.

type M int

func halves(xs ...float64) {
	for _, x := range xs {
		fmt.Print(x/2, " ")
	}
	fmt.Printf("%T\n", xs)
}

func named(xs ...M) {
	for _, x := range xs {
		fmt.Printf("%T ", x)
	}
	fmt.Println()
}

func bytes(xs ...uint8) { fmt.Println(xs, string(xs)) }

func boxes(prefix string, xs ...any) { fmt.Printf("%s %T %T %T\n", prefix, xs[0], xs[1], xs[2]) }

type R struct{}

func (R) sum(xs ...float64) float64 {
	t := 0.0
	for _, x := range xs {
		t += x / 2
	}
	return t
}

func main() {
	halves(1, 3) // 0.5 1.5 []float64
	named(1, 2)  // main.M main.M
	n := uint(9)
	bytes(1<<n, 65, 'B')       // [0 65 66]  AB
	boxes("p", 1, 'a', 2.5)    // p int int32 float64
	fmt.Println(R{}.sum(1, 2)) // 1.5
	lit := func(xs ...float64) { fmt.Println(xs[0] / 2) }
	lit(1) // 0.5
	ys := []float64{3}
	lit(ys...)                                              // 1.5
	defer func(xs ...float64) { fmt.Println(xs[0] / 4) }(1) // 0.25
}
