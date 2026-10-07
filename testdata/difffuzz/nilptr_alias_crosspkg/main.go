package main

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/nilptr_alias_crosspkg/inner"
)

// a nil *inner.Tree binds a *tree slot for `type tree = inner.Tree` —
// kin-openapi's `type originTree = yaml.OriginTree` result.

type tree = inner.Tree

func get(ok bool) (*tree, error) {
	if t, err := inner.Get(ok); err == nil {
		return t, nil
	}
	return nil, nil
}

func main() {
	a, _ := get(false)
	b, _ := get(true)
	var c *tree = (*inner.Tree)(nil)
	fmt.Println(a == nil, b.N, c == nil)
}
