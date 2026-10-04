package main

import (
	"fmt"
	"runtime"
)

func newfunc() func(int) int { return func(i int) int { return i } }

func main() {
	m := new(runtime.MemStats)
	runtime.ReadMemStats(m)
	n0 := m.Mallocs

	f := newfunc()
	_ = f(1)

	runtime.ReadMemStats(m)
	fmt.Println("stable:", m.Mallocs == n0)
	fmt.Println("sys:", m.Sys > 0)

	// two consecutive reads never differ
	runtime.ReadMemStats(m)
	alloc := m.Alloc
	runtime.ReadMemStats(m)
	fmt.Println("delta:", m.Alloc == alloc)
}
