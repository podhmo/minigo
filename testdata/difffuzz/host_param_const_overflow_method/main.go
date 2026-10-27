// A constant that does not fit the host method's int32 parameter is gc's
// compile error ("overflows"), not a silent wrap to 0.
package main

import (
	"fmt"
	"sync/atomic"
)

func main() {
	var i atomic.Int32
	i.Store(1 << 40)
	fmt.Println(i.Load())
}
