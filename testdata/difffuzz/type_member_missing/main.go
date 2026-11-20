package main

import (
	"fmt"

	dep "github.com/podhmo/minigo/testdata/difffuzz/type_member_missing/dep"
)

func main() {
	fmt.Println("before")
	var y *dep.Yes
	fmt.Println(y != nil)
	var x *dep.Nope
	fmt.Println(x)
}
