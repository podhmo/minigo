package main

import (
	"fmt"

	realpkg "github.com/podhmo/minigo/testdata/difffuzz/selnilable_scope/fakepkg"
	"github.com/podhmo/minigo/testdata/difffuzz/selnilable_scope/foo"
)

func main() {
	_ = realpkg.Nilable{}
	var x foo.T
	fmt.Println(x == nil)
}
