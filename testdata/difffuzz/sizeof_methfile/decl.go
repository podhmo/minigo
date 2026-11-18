package main

import (
	q "github.com/podhmo/minigo/testdata/difffuzz/sizeof_methfile/dep2"
)

type S struct{}

func (S) M() q.T { return q.T{} }
