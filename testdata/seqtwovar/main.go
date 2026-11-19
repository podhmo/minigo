package main

import "strings"

// a two-var range over a one-value seq is a compile error in gc; the
// vm reports it only when a value is actually yielded (driveFuncIter's
// arity check) — an empty seq just ends the loop.
func Empty() string {
	for _, s := range strings.FieldsSeq("") {
		return s
	}
	return "done"
}
