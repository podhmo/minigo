package main

import (
	"fmt"
	"runtime"
	"strings"
)

// $GOROOT/test/fixedbugs/issue5856.go — a deferred call's caller chain
// reads through the unwinder: Caller(2) inside g resolves to f's frame
// at the panic site, because Go lists gopanic between the deferred
// chain and the panicking frames.
var x = 1

func f() {
	if x == 0 {
		return
	}
	defer g()
	panic("panic")
}

func g() {
	_, file, line, _ := runtime.Caller(2)
	if !strings.HasSuffix(file, "defercall_panicsite/main.go") || line != 20 {
		fmt.Printf("BUG: defer called from %s:%d, want main.go:20\n", file, line)
		return
	}
	fmt.Println("ok")
}

func main() {
	defer func() { recover() }()
	f()
}
