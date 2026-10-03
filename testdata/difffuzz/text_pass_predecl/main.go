package main

import "fmt"

// every predeclared name is redeclarable ($GOROOT/test/rename.go).
const (
	append = 1
	false  = 11
	nil    = 23
	true   = 31
	iota   = 38
)

func main() {
	fmt.Println(append + false + nil + true + iota)
	// local shadowing works the same way.
	nil := 7
	fmt.Println(nil + true)
}
