package main

// A type spec unfolded out of an imported package kept bare leaf
// idents in the CALLER's file scope: lib.Items ([]N) answered the
// caller's own `type N string`, so `x[0] == lib.N(1)` trapped
// "mismatched types N and lib.N" where gc compares the same type. The
// unfolded expr now re-scopes its leaf names to the declaring package
// (scopeSpecType): package-level type idents become pkg.T through the
// caller's import name, and selector qualifiers re-key through the
// spec file's own imports.

import (
	"fmt"

	"github.com/podhmo/minigo/testdata/difffuzz/import_elem_callerscope/lib"
)

type N string

func main() {
	x := lib.Items{1}
	fmt.Println(x[0] == lib.N(1))

	m := lib.Dict{"k": 2}
	fmt.Println(m["k"] == lib.N(2))
}
