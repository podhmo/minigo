package main

import (
	"github.com/podhmo/minigo/testdata/lazypin/depa"
	"github.com/podhmo/minigo/testdata/lazypin/depb"
	"github.com/podhmo/minigo/testdata/lazypin/depc"
)

// UseOnlyA initializes depa on the call path; depb and depc stay inert.
func UseOnlyA() int {
	return depa.Next()
}

// TypeQueryOnly touches depb's type surface only: depb is indexed but
// its initializer must not run.
func TypeQueryOnly() int {
	var t depb.T
	return t.X
}

// MethodTouch calls a method declared in depb: the call itself is a
// value touch, so depb's initializer runs before the body.
func MethodTouch() int {
	var t depb.T
	return t.M()
}

// ValueMemberTouch reads depb's var member: depb's initializer runs.
func ValueMemberTouch() int {
	return depb.Touched
}

// BranchNotTaken has depc's call on an untaken branch: depc's
// initializer must not run when the branch is not executed.
func BranchNotTaken(cond bool) int {
	if cond {
		return depc.Get()
	}
	return depa.Next()
}

func main() {}
