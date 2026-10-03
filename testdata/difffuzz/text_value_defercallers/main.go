// Runtime.Callers during a deferred call lists the frames the in-flight
// panic already unwound between the deferred-call chain and the frames
// still live below it — Go's traceback order.
package main

import (
	"fmt"
	"runtime"
)

func run(f func()) (ok bool) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		callers := make([]uintptr, 128)
		n := runtime.Callers(0, callers)
		callers = callers[:n]
		frames := runtime.CallersFrames(callers)
		for fr, next := frames.Next(); next; fr, next = frames.Next() {
			if fr.Func.Name() == "main.main.func1" && fr.Line == 34 {
				ok = true
			}
		}
	}()
	f()
	return false
}

func main() {
	ok := run(func() {
		var v interface{ M() }
		v.M()
	})
	fmt.Println(ok)
}
