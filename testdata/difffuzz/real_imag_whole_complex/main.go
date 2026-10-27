package main

import "fmt"

// real and imag of an untyped complex constant are untyped FLOAT
// constants even when the part is a whole number: `real(complex(1,
// 0))/2` is 0.5, not the integer division 0.

func main() {
	fmt.Println(real(complex(1, 0))/2, imag(complex(0, 1))/2, real(complex(1, 2))/2) // 0.5 0.5 0.5
	fmt.Println(real(3+0i)/2, imag(4i)/8)                                            // 1.5 0.5
	const k = real(complex(1, 0))
	fmt.Println(k / 2)                                        // 0.5
	fmt.Printf("%T %T\n", real(complex(1, 0)), complex(1, 0)) // float64 complex128
}
