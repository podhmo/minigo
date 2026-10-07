package main

import (
	"fmt"
	"runtime"
)

type T struct{}

func caller() string {
	var pcs [8]uintptr
	n := runtime.Callers(0, pcs[:])
	fr := runtime.CallersFrames(pcs[:n])
	seen := 0
	for {
		fr2, more := fr.Next()
		if !more {
			return ""
		}
		if len(fr2.Function) > 5 && fr2.Function[:5] == "main." {
			seen++
			if seen == 2 {
				return fr2.Function
			}
		}
	}
}

func (t *T) P() { fmt.Println(caller()) }

func (t T) V() { fmt.Println(caller()) }

func main() {
	t := &T{}
	t.P()
	m := t.P
	m()
	// a value-receiver method keeps the T.M spelling.
	u := T{}
	u.V()
}
