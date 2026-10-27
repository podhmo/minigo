// An untyped constant passed to a host func or method converts at the
// parameter's type, not the default int: values above MaxInt64 reach
// uint64/uintptr parameters, and float32 parameters round at float32.
package main

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"sync/atomic"
	"time"
)

const big63 = 1 << 63

func main() {
	var u atomic.Uint64
	u.Store(1<<64 - 1)
	fmt.Println(u.Load())
	fmt.Println(u.CompareAndSwap(1<<64-1, 7), u.Load())
	fmt.Println(u.Add(1 << 63))

	var up atomic.Uintptr
	fmt.Println(up.Add(1<<63), up.CompareAndSwap(1<<63, 1<<64-1), up.Load())

	var i32 atomic.Int32
	fmt.Println(i32.Add(-1), i32.CompareAndSwap(-1, 1<<31-1), i32.Load())

	var n uint = 63
	fmt.Println(strconv.FormatUint(1<<63, 10), strconv.FormatUint(1<<n, 10))
	fmt.Println(strconv.FormatUint(big63, 16), string(strconv.AppendUint(nil, 1<<64-1, 10)))
	fmt.Println(new(big.Int).SetUint64(1<<64 - 1).String())

	fmt.Println(math.Float32frombits(1<<31|1<<30), math.Float64frombits(1<<62|1<<61|1<<60))
	fmt.Println(math.Float32bits(0.1), math.Float32bits(1<<24+1))

	d := 1500 * time.Millisecond
	fmt.Println(d.Round(1e9), d.Truncate(1e9))
}
