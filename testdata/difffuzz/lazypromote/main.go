package main

import "fmt"

// P4-2c lazy promotion: a receiver whose static type the compiler
// cannot prove stays a plain slot — the first ref-taking select
// promotes it on demand, and later reads, &x, and captures all
// observe the same cell.
type C struct{ N int }

func (c *C) Bump() { c.N++ }

// id's instantiation result mentions the type parameter, so `x := id(v)`
// binds no declared type — the receiver select is unprovable.
func id[T any](v T) T { return v }

func main() {
	// plain slot + pointer-receiver call: the runtime promotes the
	// slot lazily and the write lands on the value.
	a := id(C{})
	a.Bump()
	a.Bump()
	fmt.Println(a.N) // 2

	// &x on a classified-boxed slot reads the pre-allocated cell —
	// writes through it and method calls agree on one identity.
	b := C{}
	p := &b
	b.Bump()
	p.Bump()
	fmt.Println(b.N, p.N) // 2 2

	// a captured slot boxes at compile time — the closure and the
	// promoted receiver select share the one cell.
	c := id(C{})
	f := func() int { return c.N }
	c.Bump()
	fmt.Println(f(), c.N) // 1 1
}
