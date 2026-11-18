package main

import dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_promoted/dep1"

// Inner's field type spells `dep` — in THIS file dep1.
type Inner struct{ Arr [4]dep.T }
