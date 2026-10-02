package main

import "fmt"

type U64 uint64
type U32 uint32

func main() {
	// equality between differently-typed dynamic values is lawful —
	// `got != u32` is true, not a "mismatched types" error ($GOROOT/test/convT2X.go).
	var got any = U64(5)
	fmt.Println(got != U32(5))
	fmt.Println(got == U32(5))
	fmt.Println(got == U64(5))
	fmt.Println(got != U64(6))
}
