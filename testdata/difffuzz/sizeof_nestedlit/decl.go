package main

import dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_nestedlit/dep1"

var V = dep.T{{1}, {2}} // the inner literals' types are elided
