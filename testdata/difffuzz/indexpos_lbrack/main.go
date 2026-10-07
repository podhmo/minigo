package main

import (
	"fmt"
	"runtime"
	"strings"
)

var i = 9
var sink int
var sinkS []int

func main() {
	shouldPanic(func() {
		var a [3]int
		sink = a /*line :999999:1*/ [ /*line :200:1*/ i]
	})
	shouldPanic(func() {
		var a [3]int
		a /*line :999999:1*/ [ /*line :500:1*/ i] = 1
	})
	shouldPanic(func() {
		var a []int
		sinkS = a /*line :999999:1*/ [ /*line :1200:1*/ i:]
	})
	fmt.Println("ok")
}

func shouldPanic(f func()) {
	defer func() {
		if recover() == nil {
			panic("did not panic")
		}
		var pcs [10]uintptr
		n := runtime.Callers(1, pcs[:])
		iter := runtime.CallersFrames(pcs[:n])
		buf := ""
		for {
			frame, more := iter.Next()
			buf += fmt.Sprintf("%s:%d %s\n", frame.File, frame.Line, frame.Function)
			if !more {
				break
			}
		}
		if !strings.Contains(buf, "999999") {
			panic("marker line missing:\n" + buf)
		}
	}()
	f()
}
