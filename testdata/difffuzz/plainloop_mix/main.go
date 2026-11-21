package main

import "fmt"

// namedResult exercises named result slots mutated by a defer — the
// defer's capture boxes the slots while explicit `return a, b`
// stores straight into them.
func namedResult(fail bool) (a, b int) {
	defer func() { a++ }()
	if fail {
		return 0, 0
	}
	return 40, 2
}

func main() {
	// loop variable captured by a closure: Go 1.22+ rebinds i per
	// iteration, so the closures see 0 1 2, while the sibling counter
	// stays a plain slot.
	var fns []func() int
	total := 0
	for i := 0; i < 3; i++ {
		total += i
		fns = append(fns, func() int { return i })
	}
	for _, f := range fns {
		fmt.Print(f(), " ")
	}
	fmt.Println(total)

	// range variable per-iteration capture (Go 1.22+).
	var g []func() int
	for _, v := range []int{10, 20, 30} {
		g = append(g, func() int { return v })
	}
	for _, f := range g {
		fmt.Print(f(), " ")
	}
	fmt.Println()

	// multi-assign swap across a boxed slot (p) and a plain one (q).
	p, q := 1, 2
	_ = &p
	p, q = q, p
	fmt.Println(p, q)

	// multi-assign from function results into a boxed slot and a new
	// plain one.
	r := 5
	_ = &r
	r, s := divmod(17, 5)
	fmt.Println(r, s)

	fmt.Println(namedResult(false))
	fmt.Println(namedResult(true))
}

func divmod(a, b int) (int, int) { return a / b, a % b }
