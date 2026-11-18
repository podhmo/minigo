package main

import (
	o "github.com/podhmo/minigo/testdata/difffuzz/gate_nilaliasfile/inner"
)

// S is declared through this file's import alias; a compare compiled
// from another file must still resolve `o` here.
var S o.Set[int]
