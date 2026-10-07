package main

import (
	"fmt"
	"runtime"
	"strings"
)

func main() {
//line :1
	f, l, _ := caller()
	fmt.Printf("f=%q l=%d\n", f, l)
//line foo.go:1
	f, l, _ = caller()
	fmt.Printf("foo=%v l=%d\n", strings.HasSuffix(f, "/foo.go"), l)
//line bar.go:10:20
	f, l, _ = caller()
	fmt.Printf("bar=%v l=%d\n", strings.HasSuffix(f, "/bar.go"), l)
//line :11:22
	f, l, _ = caller()
	fmt.Printf("bar=%v l=%d\n", strings.HasSuffix(f, "/bar.go"), l)
}
func caller() (string, int, bool) {
	_, f, l, ok := runtime.Caller(1)
	return f, l, ok
}
