package main

import "fmt"

// A panic raised inside a deferred call unwinds on top of the
// panicking frames — the owner's remaining defers still run, and a
// surviving defer's recover() sees the NEW panic value (gc chains
// `panic: first` then `panic: second` on the fatal path). The same
// holds for a deferred host-side runtime panic.
// Bug: minigo's failProc fired while unwinding the deferred panic —
// the owner frame is already popped and no longer counts in
// len(v.frames) — so procExit killed the pending defers mid-drain.

func f() {
	defer func() {
		fmt.Println("f-inner recover:", recover())
	}()
	defer func() {
		fmt.Println("f-mid ran")
		panic("f-second")
	}()
	panic("f-first")
}

func g() {
	defer func() {
		fmt.Println("g-inner recover:", recover())
	}()
	defer func() {
		var xs []int
		_ = xs[9] // host-side runtime panic inside the deferred call
	}()
	panic("g-first")
}

func main() {
	f()
	fmt.Println("f returned")
	g()
	fmt.Println("unreachable")
}
