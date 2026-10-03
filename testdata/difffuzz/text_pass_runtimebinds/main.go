// run

// Reduced pin for the runtime-package bindings the $GOROOT/test
// sweep needed: scalar vars, no-op funcs, runtime.Error asserts,
// MemStats reads, package-member assignment, and a bytes.Buffer
// declared by var.

package main

import (
	"bytes"
	"fmt"
	"runtime"
)

func maybeFail() {
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(runtime.Error); ok {
				fmt.Println("runtime.Error")
			} else {
				fmt.Println("other")
			}
		}
	}()
	var p *int
	_ = *p
}

func main() {
	if runtime.Compiler != "gc" {
		panic("compiler")
	}
	if len(runtime.GOROOT()) == 0 {
		panic("goroot")
	}
	runtime.SetFinalizer(new(int), nil)
	runtime.KeepAlive(new(int))

	runtime.MemProfileRate = 1024
	if runtime.MemProfileRate != 1024 {
		panic("memprofilerate")
	}

	m := new(runtime.MemStats)
	runtime.ReadMemStats(m)
	if m.Sys == 0 {
		panic("sys")
	}
	fmt.Println("stats ok")

	maybeFail()

	var buf bytes.Buffer
	buf.WriteString("buf")
	if buf.String() != "buf" {
		panic("buffer")
	}
	fmt.Println("done")
}
