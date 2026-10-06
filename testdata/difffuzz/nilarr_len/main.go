package main

import "fmt"

// len(*p) on an addressable nil *[N]T var is a static constant — the
// compiler does not dereference it (but *func() is not addressable and
// derefs). $GOROOT/test/fixedbugs/issue72844.go.

var nilPtrVar *[4]int

func main() {
	fmt.Println(len(*nilPtrVar))
}
