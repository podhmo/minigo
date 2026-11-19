// Package vetstub lives in a directory whose name differs from the
// package clause, so an unaliased import binds `vetstub`, not `vetstub2`.
package vetstub

func Unreg2() { panic("minigo intrinsic") }
