package main

import (
	"fmt"
	"runtime"
)

type call struct {
	frame runtime.Frame
}

func caller() call {
	var pcs [3]uintptr
	n := runtime.Callers(1, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	frame, _ := frames.Next()
	frame, _ = frames.Next()
	return call{frame: frame}
}

func main() {
	fmt.Println(caller().frame.Function)
}
