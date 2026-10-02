// Package errfoob declares Same — collides with errfooa.Same by leaf name.
package errfoob

type Same struct{ N int }

func (e *Same) Error() string { return "errfoob.Same" }
