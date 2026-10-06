package main

import "fmt"

// `var a, b = x.(T)` is a multi-valued (comma-ok) assertion: a gets the
// zero value, b the ok flag — no panic. $GOROOT/test/fixedbugs/issue53619.go.

var c = b
var d = a

var a, b any = any(nil).(bool)

func main() {
	if c != false {
		panic(c)
	}
	if d != false {
		panic(d)
	}
	fmt.Println("ok")
}
