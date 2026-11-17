package main

import "fmt"

func main() {
	const n = 4
	type T struct {
		a [n]int
		b [n * 2]string
	}
	var t T
	t.a[3] = 9
	t.b[7] = "z"
	fmt.Println(len(t.a), t.a[3], len(t.b), t.b[7])
	// a nested local type referencing the same const in a pointer field
	type U struct {
		p *[n]int
	}
	var u U
	u.p = &t.a
	fmt.Println(len(*u.p), u.p[2])
	// local array type decl itself
	type A [n]int
	var a A
	a[1] = 5
	fmt.Println(len(a), a[1])
}
