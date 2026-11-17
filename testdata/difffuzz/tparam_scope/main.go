package main

import (
	"fmt"

	tplib "github.com/podhmo/minigo/testdata/difffuzz/tparam_scope_lib"
)

// The caller's own N must not shadow the leaf N of the bound
// argument's spec (lib.Items is []lib.N).
type N string

func g[S ~[]tplib.N](s S) bool { return s[0] == tplib.N(1) }

func main() { fmt.Println(g[tplib.Items](tplib.Items{1})) }
