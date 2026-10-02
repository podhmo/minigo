package main

import "fmt"

func shouldPanic(f func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	f()
	return
}

func main() {
	var p *[10]int
	// len/cap of an array pointer is a constant — the nil is never read
	fmt.Println(len(p), cap(p))

	// index-only range over nil *[N]T iterates 0..N-1
	s := 0
	for i := range p {
		s += i
	}
	fmt.Println(s)

	// two-var range derefs p[i] — panics
	fmt.Println(shouldPanic(func() {
		for _, v := range p {
			_ = v
		}
	}))
	// direct element read also derefs
	fmt.Println(shouldPanic(func() { _ = p[0] }))
}
