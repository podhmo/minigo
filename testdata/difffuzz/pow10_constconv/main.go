package main

import "fmt"

// Untyped float consts that fit uint64 but not int64 (1e19) must compare
// equal in switch case labels. $GOROOT/test/fixedbugs/issue43480.go.

func isPow10(x uint64) bool {
	switch x {
	case 1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9,
		1e10, 1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19:
		return true
	}
	return false
}

func main() {
	var x uint64 = 1
	for i := 0; i < 20; i++ {
		if !isPow10(x) {
			fmt.Println("miss:", x)
			return
		}
		x *= 10
	}
	fmt.Println("ok")
}
