package main

import dep "github.com/podhmo/minigo/testdata/difffuzz/sizeof_initfunccall/dep1"

var V = dep.F(0) // dep1.F is a func — V : dep1.T
