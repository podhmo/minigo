// Package errfooa declares Same — a same-named error type for the
// errors.As cross-package identity check in testdata/fuzzfix.
package errfooa

type Same struct{ N int }

func (e *Same) Error() string { return "errfooa.Same" }
