package main

import "fmt"

// A typed float constant evaluates in its width: `float32(c)` rounds
// the constant's payload to float32 precision, so `float64(float32
// (0.01))` reads the float32 rounding, not 0.01's float64 nearest
// (corpus bug470). The tag keeps the constant domain — only the value
// narrows (complex64 rounds each half to float32 the same way).

const (
	F32 = 0.00999999977648258209228515625
	F64 = 0.01000000000000000020816681711721685132943093776702880859375
)

func main() {
	fmt.Println(float64(float32(0.01)) == F32, float64(float32(0.01)) != F64)
	fmt.Println(float32(0.01))     // %v still spells the float32
	fmt.Println(float32(16777217)) // int constants round to the width too
	fmt.Println(complex64(0.01 + 0.02i))
	type MyF32 float32
	fmt.Println(float64(MyF32(0.01)) == F32) // named float32 rounds the same
}
