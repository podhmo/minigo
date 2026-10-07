package main

import "fmt"

// Storing an array into its address must overwrite the existing backing
// in place — `s := x[:]` and (*[N]T)(s) conversions share that storage,
// so a store that rebinds the variable detaches them (Go: they keep
// aliasing). Covers `x = arr`, `*p = arr` on &x, and the
// slice-to-array-pointer conversion view.

func main() {
	var x [4]byte
	s := x[:]
	x = [4]byte{9, 9, 9, 9}
	fmt.Println(s[0], x[0]) // 9 9

	var y [8]byte
	sy := y[:]
	p := &y
	*p = [8]byte{7, 7, 7, 7, 7, 7, 7, 7}
	fmt.Println(sy[0], y[0]) // 7 7

	var z [8]byte
	sz := z[:]
	q := (*[4]byte)(sz[1:5])
	*q = [4]byte{8, 8, 8, 8}
	fmt.Println(z) // [0 8 8 8 8 0 0 0]

	// element store through the conversion view still works afterwards
	(*q)[0] = 5
	fmt.Println(z[1]) // 5

	// multi-assign through the same pointer lands on the shared backing
	var w [4]byte
	sw := w[:]
	pw := &w
	*pw, sw[0] = [4]byte{9, 9, 9, 9}, 7
	fmt.Println(sw[0], w[0], w[1]) // 7 7 9
}
