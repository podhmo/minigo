package main

import (
	"fmt"
	"sync/atomic"
)

// sync/atomic's typed functions assign their operands to the word type
// the name spells: AddInt32(&x, 1) leaves an int32 in x, and a
// non-constant shift operand wraps at that width (`1<<(n+24)+3` adds 3
// to a uint32, `1<<(n+23)` is MinInt32 as an int32).

func main() {
	var x int32
	atomic.AddInt32(&x, 1)
	fmt.Printf("%T %v\n", x, x) // int32 1
	n := 8
	var c uint32
	fmt.Println(atomic.AddUint32(&c, 1<<(n+24)+3), c) // 3 3
	var d int32
	r := atomic.AddInt32(&d, 1<<(n+23))
	fmt.Printf("%T %v %v\n", r, r, d) // int32 -2147483648 -2147483648
	var u uint32 = 5
	atomic.StoreUint32(&u, 7)
	old := atomic.SwapUint32(&u, 9)
	fmt.Printf("%T %v %T %v\n", old, old, u, u)                     // uint32 7 uint32 9
	fmt.Println(atomic.CompareAndSwapUint32(&u, 9, 1<<(n+24)+2), u) // true 2
	var w uint64
	atomic.AddUint64(&w, ^uint64(0))
	fmt.Println(w, atomic.LoadUint64(&w)) // 18446744073709551615 18446744073709551615
	var y uint32 = 1
	atomic.AddUint32(&y, ^uint32(0))
	fmt.Println(y) // 0
	var a int64 = 6
	fmt.Println(atomic.AndInt64(&a, 3))   // 6
	fmt.Println(atomic.OrInt64(&a, 8), a) // 2 10
}
