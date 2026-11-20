package main

import "fmt"

// Typed float/complex constant expressions round every operation to
// the type, not just the final conversion: `float32(0.1)+float32(0.2)`
// is the float32 sum, `1e300*float64(10)/1e10` rounds each step's
// product and quotient to float64 (TODO: per-step rounding).
func main() {
	fmt.Println(float64(float32(1) / 3))
	fmt.Println(float32(0.1)+float32(0.2) == 0.3)
	fmt.Println(1e300 * float64(10) / 1e10)
	var f float32 = float32(0.1) + float32(0.2)
	fmt.Println(f == 0.3, f)
	c := complex64(complex(1, 2)) + complex64(complex(1e30, 0))
	fmt.Println(float64(real(c)))
	fmt.Println(complex64(0.1)/complex64(0.3) == complex64(1)/complex64(3))
}
