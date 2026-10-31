package main

import (
	"fmt"
	"time"
)

// Since go1.23 no fire sent before Stop/Reset is receivable afterwards,
// and a fired-but-unreceived timer still counts as active for Stop and
// Reset. The proxy feed must not leave the old value in its buffer.

func probe(tag string, c <-chan time.Time) {
	select {
	case <-c:
		fmt.Println(tag, "stale")
	default:
		fmt.Println(tag, "none")
	}
}

func main() {
	tm := time.NewTimer(time.Nanosecond)
	time.Sleep(10 * time.Millisecond)
	fmt.Println("timer reset active:", tm.Reset(time.Hour))
	probe("timer reset:", tm.C)
	fmt.Println("timer stop pending:", tm.Stop())
	fmt.Println("timer stop again:", tm.Stop())

	tm2 := time.NewTimer(time.Nanosecond)
	time.Sleep(10 * time.Millisecond)
	fmt.Println("timer stop fired:", tm2.Stop())
	probe("timer stop:", tm2.C)
	fmt.Println("timer reset after stop:", tm2.Reset(time.Nanosecond))
	<-tm2.C
	fmt.Println("timer stop received:", tm2.Stop())

	tk := time.NewTicker(time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	tk.Reset(time.Hour)
	probe("ticker reset:", tk.C)

	tk2 := time.NewTicker(time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	tk2.Stop()
	probe("ticker stop:", tk2.C)
}
