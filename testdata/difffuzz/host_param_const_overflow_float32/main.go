// A constant beyond float32's range passed to a float32 parameter is gc's
// compile error ("overflows"), not +Inf's bit pattern.
package main

import (
	"fmt"
	"math"
)

func main() {
	fmt.Println(math.Float32bits(1e300))
}
