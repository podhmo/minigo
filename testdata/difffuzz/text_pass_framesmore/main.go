package main

import (
	"fmt"
	"runtime"
)

func f() {
	p := make([]uintptr, 16)
	n := runtime.Callers(0, p)
	frames := runtime.CallersFrames(p[:n])
	total, trues, empties := 0, 0, 0
	for {
		frame, more := frames.Next()
		total++
		if more {
			trues++
		}
		if frame.Function == "" {
			empties++
		}
		if !more {
			break
		}
	}
	// the last real frame reports more=false and no empty frame trails
	fmt.Println(trues == total-1, empties == 0)
	_, more := frames.Next()
	fmt.Println(more)
}

func main() { f() }
