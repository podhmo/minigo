package main

// Timer and ticker channels are synchronous since Go 1.23: len and cap
// report 0 even though minigo serves them through a 1-buffered feed
// proxy. Script channels of time.Time keep their own buffer.

import (
	"fmt"
	"time"
)

func main() {
	t := time.NewTimer(time.Hour)
	fmt.Println(cap(t.C), len(t.C))
	t.Stop()
	tk := time.NewTicker(time.Hour)
	fmt.Println(cap(tk.C), len(tk.C))
	tk.Stop()
	a := time.After(time.Hour)
	fmt.Println(cap(a), len(a))

	fired := time.NewTimer(time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	fmt.Println(cap(fired.C), len(fired.C))
	<-fired.C

	own := make(chan time.Time, 3)
	own <- time.Time{}
	fmt.Println(cap(own), len(own))
}
