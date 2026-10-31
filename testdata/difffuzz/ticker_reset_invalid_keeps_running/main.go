package main

import (
	"fmt"
	"time"
)

// Ticker.Reset with a non-positive interval panics before touching the
// ticker; once recovered, the ticker must keep ticking.

func main() {
	tk := time.NewTicker(time.Millisecond)
	defer tk.Stop()
	for _, d := range []time.Duration{0, -time.Second} {
		func() {
			defer func() { fmt.Println("recovered:", recover()) }()
			tk.Reset(d)
		}()
		select {
		case <-tk.C:
			fmt.Println("ticker still running")
		case <-time.After(time.Second):
			fmt.Println("ticker stopped after invalid Reset")
		}
	}
}
