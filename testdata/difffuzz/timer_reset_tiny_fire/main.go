package main

import (
	"fmt"
	"time"
)

// Reset to a tiny duration can fire the real timer before the proxy
// feed re-arms; the superseded pump then holds the only fire and must
// still deliver it, or the receive below parks forever.

func main() {
	for i := 0; i < 300; i++ {
		tm := time.NewTimer(time.Hour)
		tm.Reset(time.Duration(1))
		<-tm.C
	}
	fmt.Println("timer ok")
	for i := 0; i < 300; i++ {
		tk := time.NewTicker(time.Hour)
		tk.Reset(time.Duration(1))
		<-tk.C
		tk.Stop()
	}
	fmt.Println("ticker ok")
}
