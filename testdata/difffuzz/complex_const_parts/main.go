package main

import "fmt"

// Complex constants keep Go's constant rules: real/imag of an untyped
// complex constant are untyped FLOAT constants (`real(c1)/2` is 0.5),
// of a typed one the matching float type (float32 for complex64),
// complex(re, im) over constants is itself a constant, and a complex
// constant with a zero imaginary part converts to int or float.

const c1 = 1 + 2i
const k = complex(3, 0)
const k2 = complex(1, 2)

func main() {
	fmt.Println(real(c1)/2, imag(c1)/4)                   // 0.5 0.5
	fmt.Printf("%T %T\n", real(c1), real(complex64(c1)))  // float64 float32
	fmt.Println(real(complex64(c1)), imag(complex64(c1))) // 1 2
	var i int = k
	var f float64 = k
	fmt.Println(i, f, i+1) // 3 3 4
	const r = real(c1)
	var g float32 = r
	fmt.Println(g / 3) // 0.33333334
	x := k
	fmt.Println(k, k2*k2, x)     // (3+0i) (-3+4i) (3+0i)
	fmt.Printf("%T %T\n", x, k2) // complex128 complex128
	var c complex64 = complex(1, 2)
	fmt.Printf("%T %v %T\n", c, c, real(c)) // complex64 (1+2i) float32
}
