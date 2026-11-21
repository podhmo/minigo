package main

import (
	"fmt"
	"unsafe"

	"github.com/podhmo/minigo/testdata/difffuzz/gate_recvscope/inner"
)

const N = 1

func main() {
	fmt.Println(unsafe.Sizeof(inner.Durations[0]), unsafe.Sizeof(inner.Arrays[0]), unsafe.Alignof(inner.Arrays[0]), N)
	inner.DurationCh <- 0
	select {
	case d := <-inner.DurationCh:
		fmt.Println(unsafe.Sizeof(d), unsafe.Alignof(d))
	}
	inner.ArrayCh <- inner.Arrays[0]
	select {
	case a := <-inner.ArrayCh:
		fmt.Println(unsafe.Sizeof(a), unsafe.Alignof(a))
	}
	inner.ResetHostCh()
	select {
	case now := <-inner.HostCh:
		fmt.Println(unsafe.Sizeof(now), unsafe.Alignof(now))
	}
}
