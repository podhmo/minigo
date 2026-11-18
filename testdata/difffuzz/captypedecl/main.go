package main

import "fmt"

func f() any {
	type T []T
	// the closure's capture boxes T's slot — the typedef lookup for
	// T's element must still reach it through the boxed slot.
	func() { _ = T(nil) }()
	return T{{}}
}

func g() any {
	type E int
	type S struct{ N E }
	func() { _ = E(0) }()
	return S{N: 7}
}

func main() {
	fmt.Println(f())
	fmt.Println(g())
}
