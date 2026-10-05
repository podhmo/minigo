package app

// StatusAlias is an alias for Status — no directive targets it, so
// stringer never generates for it.
type StatusAlias = Status

// Count is a named int without enum consts: a directive aiming at it
// reports "no enum consts" instead of emitting an empty switch.
type Count int

// Ratio is not an enum shape at all.
type Ratio float64

// A directive does not have to sit above its type — position only feeds
// GOLINE. This one runs like any other.
//
//minigo:generate ../tools/stringer -type=Level
type Level int

const (
	Beginner Level = iota
	Intermediate
	Advanced
)
