package main

import "fmt"

// Slice-bounds panic wording follows the container: arrays and strings
// say "with length N", slices "with capacity N" (corpus issue30116).
func main() {
	try := func(f func()) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Println(r)
			}
		}()
		f()
	}
	s := "123"
	a := [3]int{1, 2, 3}
	sl := []int{1, 2, 3}
	lo, hi := 0, 4
	neg, two := -1, 2
	try(func() { _ = s[lo:hi] })
	try(func() { _ = a[lo:hi] })
	try(func() { _ = sl[lo:hi] })
	try(func() { _ = a[neg:two] })
	try(func() { _ = a[lo:two:hi] })
	try(func() { _ = sl[lo:two:hi] })
	try(func() { _ = a[neg:hi] })
}
