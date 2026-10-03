package main

import "fmt"

// a defined type whose underlying is `any` still accepts any value:
// assignability is interface satisfaction, not tag identity.
type Token any

type S struct{ x int }

func f() Token { return S{1} }

type AnyBox any

func main() {
	var t Token = "str"
	var b AnyBox = []int{1}
	fmt.Println(t != nil, b != nil, f() != nil)
}
