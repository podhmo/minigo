package main

import "fmt"

// Array lengths spelled by a package const fold identically inside any
// type-expression position — the eval is emitted at each typedef's
// const, not only at a top-level [N]T.

const N = 3

func main() {
	var m map[string][N]int = map[string][3]int{"a": {1, 2, 3}}
	fmt.Println(m["a"][0], len(m["a"]))

	var c chan [N]int = make(chan [3]int, 1)
	c <- [3]int{7, 8, 9}
	fmt.Println(len(<-c))

	var p *[N]int = &[3]int{4, 5, 6}
	fmt.Println(p[1])

	f := func(a [N]int) int { return a[2] }
	fmt.Println(f([3]int{1, 2, 9}))

	var s struct{ F [N]int }
	s = struct{ F [3]int }{F: [3]int{5, 6, 7}}
	fmt.Println(s.F[0])
}
