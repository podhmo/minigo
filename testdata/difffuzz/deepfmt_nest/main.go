package main

import "fmt"

// $GOROOT/test/fixedbugs/issue29264.go — fmt renders every level of a
// deeply nested composite; minigo capped fmtValue at depth 8 and
// printed "[[[[[[[[[...]]]]]]]]]" where Go prints all levels.
func main() {
	a := [][][][][][][][][][][][][][][][][][][][]int{{{{{{{{{{{{{{{{{{{{42}}}}}}}}}}}}}}}}}}}}
	fmt.Println(a)
}
