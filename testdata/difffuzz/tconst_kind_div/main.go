package main

import "fmt"

// A typed constant expression computes in its type's kind: `float64(7)`
// is spelled with the integer 7, yet `float64(7) / 2` is the float64
// quotient 3.5 — the exact-domain fold must not divide the Int payloads.

type MyF float64

const CF float64 = 7

func main() {
	fmt.Println(float64(7)/2, float32(7)/2, CF/2, MyF(7)/2)      // 3.5 3.5 3.5 3.5
	fmt.Println(complex128(7)/2, 7/float64(2), float64(1)/3)     // (3.5+0i) 3.5 0.333…
	fmt.Println(int(7.0)/2, float64(7)/2*2, float64(7)/2 == 3.5) // 3 7 true
}
