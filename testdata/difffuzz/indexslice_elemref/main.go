package main

import "fmt"

// a[i][:] binds the element's storage like &a[i] — the index produces
// an lvalue, not a value copy, so slices of array elements alias the
// container's backing: element stores and whole-array overwrites both
// write through. Non-refable bases keep value semantics: map elements
// are unaddressable in gc (compile error for arrays; legal slicing
// for strings — covered here), slice elements share their backing
// either way, and string elements subslice as usual.

func main() {
	var x [2][2]int
	s := x[0][:]
	x[0][0] = 5 // element store through the container reaches the view
	fmt.Println(s[0], x[0][0])
	x = [2][2]int{{9, 9}, {9, 9}} // whole-array overwrite reaches it too
	fmt.Println(s[0], s[1], x[1][0])

	// pointer-to-array element slice
	p := &x
	sp := (*p)[1][:]
	x[1][0] = 4
	fmt.Println(sp[0])

	// slice-of-slices: the inner backing was always shared
	ss := [][]int{{1, 2}}
	s2 := ss[0][:]
	ss[0][0] = 7
	fmt.Println(s2[0])

	// array of strings: a[i][lo:] subslices the element
	var as [1]string
	as[0] = "hello"
	fmt.Println(as[0][1:])

	// map of strings: m[k][lo:] is legal (no array element involved)
	ms := map[int]string{0: "hello"}
	fmt.Println(ms[0][1:])
}
