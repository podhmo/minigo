package main

// A constant shift keeps its untypedness even when the count is a
// TYPED constant: `const a = 1 << n` stays an untyped constant and
// adopts the compared operand's type. staticOpTyp's shift branch
// typed every typed-count shift by the left operand's default (int),
// so the compile-time compare gate reported `a == b` as "mismatched
// types int and uint" where gc evaluates it true. Local consts needed
// a const-bound mark on their binding — a typed const looked like a
// var to the constant check.

import "fmt"

const n uint = 1
const a = 1 << n

func main() {
	var b uint = 2
	fmt.Println(a == b)

	const m uint = 2
	fmt.Println(1 << m)

	var v uint = 3
	fmt.Println(1 << v)
}
