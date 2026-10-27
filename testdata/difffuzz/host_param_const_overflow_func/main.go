// A negative constant passed to a host func's uint64 parameter is gc's
// compile error ("overflows"), not a wrap to 18446744073709551615.
package main

import (
	"fmt"
	"strconv"
)

func main() {
	fmt.Println(strconv.FormatUint(-1, 10))
}
