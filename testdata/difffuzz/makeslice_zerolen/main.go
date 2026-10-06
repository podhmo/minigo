package main

import "fmt"

// make([]T, maxInt) on a zero-size element type allocates 0 bytes and
// must succeed. $GOROOT/test/fixedbugs/issue29190.go.

const maxInt = int(^uint(0) >> 1)

func main() {
	s := make([]struct{}, maxInt)
	fmt.Println(len(s) == maxInt)
}
