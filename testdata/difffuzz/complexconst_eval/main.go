package main

import "fmt"

// complex()/imag() on untyped constants must stay in exact rational
// arithmetic — `complex(0,2)/3` must not round through float64.
// $GOROOT/test/fixedbugs/issue43908.go.

const ulp1 = imag(1i + 2i/3 - 5i/3)
const ulp2 = imag(1i + complex(0, 2)/3 - 5i/3)

func main() {
	fmt.Println(ulp1, ulp2, ulp1 == ulp2)
}
