//go:build go1.19

package main

// the tag replaces the module lang with max(go1.19, go1.21) = go1.21 —
// range-over-int (go1.22) still fails, and the error reports the go.mod
// -lang like gc.

func Loop() int {
	n := 0
	for i := range 10 {
		n += i
	}
	return n
}

func main() {}
