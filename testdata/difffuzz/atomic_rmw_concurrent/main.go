package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// sync/atomic's pointer functions are indivisible: 200 goroutines
// adding 50 times each lose no update, and a Load+CompareAndSwap retry
// loop converges like Go's.

func main() {
	var n int32
	var u uint64
	var m int64
	var wg sync.WaitGroup
	for g := 0; g < 200; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				atomic.AddInt32(&n, 1)
				atomic.AddUint64(&u, 2)
				for {
					old := atomic.LoadInt64(&m)
					if atomic.CompareAndSwapInt64(&m, old, old+3) {
						break
					}
				}
			}
		}()
	}
	wg.Wait()
	fmt.Println(n, u, m) // 10000 20000 30000
}
