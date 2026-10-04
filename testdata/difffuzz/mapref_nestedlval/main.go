package main

import "fmt"

type T struct {
	i int64
	f float32
}

func main() {
	// map of slices: m[k][i] = v writes through the stored backing.
	mspa := make(map[string][]string)
	mspa["a"] = []string{"x", "y"}
	mspa["a"][1] = "z"
	fmt.Println(mspa["a"])

	// map of pointers: m[k].f selects through the stored *T.
	mipT := make(map[int]*T)
	mipT[0] = &T{i: 1}
	mipT[0].i += 1
	mipT[0].f = 5.5
	fmt.Println(mipT[0].i, mipT[0].f)

	// comma-ok still distinguishes a missing key.
	if p, ok := mipT[9]; ok || p != nil {
		fmt.Println("unexpected")
	}
}
