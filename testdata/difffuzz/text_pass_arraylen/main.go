package main

import "fmt"

const N = 5

type pair [2]int

func main() {
	// `[N]uint8` with a const-name length: arrayLen resolved the name
	// through Materialize, which returns NIL for const decls — the
	// array came out len 0 ($GOROOT/test/copy.go).
	var a [N]uint8
	fmt.Println(len(a), cap(a))
	for i := range a {
		a[i] = uint8('x' + i)
	}
	fmt.Println(string(a[:]))

	var b [N * 2]int
	fmt.Println(len(b))

	var p pair
	p[1] = 42
	fmt.Println(p)

	c := [N]int{1, 2, 3}
	fmt.Println(len(c), c)
}
