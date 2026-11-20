package main

import (
	"fmt"
)

// %w misuse prints gc's bad-verb marker: a nil or non-error operand is
// %!w(...), a missing operand %!w(MISSING); a valid error still wraps.
type P struct{}

func main() {
	fmt.Println(fmt.Errorf("x %w", nil))
	fmt.Println(fmt.Errorf("x %w %w", nil))
	fmt.Println(fmt.Errorf("x %w", 3))
	fmt.Println(fmt.Errorf("x %w"))
	fmt.Println(fmt.Errorf("a %w b %w", fmt.Errorf("e1"), fmt.Errorf("e2")))
	var p *P
	fmt.Println(fmt.Errorf("x %w", p))
	fmt.Println(fmt.Errorf("x %w", "s"))
	fmt.Println(fmt.Errorf("%w", fmt.Errorf("ok")))
	fmt.Println(fmt.Errorf("%[1]w %[1]w", fmt.Errorf("e")))
	// a shared operand index must not corrupt the sibling verb, and a
	// Stringer operand's method stays silent inside the marker like
	// gc's method-suppressed inner %v; precision cuts the inner text.
	fmt.Println(fmt.Errorf("%[1]d %[1]w", 3))
	fmt.Println(fmt.Errorf("%.2w", "abc"))
	fmt.Println(fmt.Errorf("x %w", S{}))
	// a bad %w's `*` width/precision operand reads like gc: integers
	// pad or cut the inner value, non-integers print a BADWIDTH /
	// BADPREC marker ahead of it, and a shared star slot keeps its
	// value for the sibling verb.
	fmt.Println(fmt.Errorf("%[1]*[1]w", "x"))
	fmt.Println(fmt.Errorf("%[2]*[1]w", "x", 5))
	fmt.Println(fmt.Errorf("%[3]*[2]w %[1]d", 7, "x", 4))
	fmt.Println(fmt.Errorf("%.*w", "x", 5))
	fmt.Println(fmt.Errorf("%[2]*[1]w %[2]d", "x", 4))
	// an out-of-range index prints %!w(BADINDEX), a missing implicit
	// one %!w(MISSING); once any [n] reorders, unused operands print
	// no EXTRA, and a missing `*` operand is BADWIDTH not a trap.
	fmt.Println(fmt.Errorf("%[9]w", "x"))
	fmt.Println(fmt.Errorf("%[9]w %d", "x", 3))
	fmt.Println(fmt.Errorf("%*w", "x"))
	fmt.Println(fmt.Errorf("%[2]*[1]w", "x"))
	fmt.Println(fmt.Errorf("%[2]*w", "x"))
	// [0] is a bad index like an out-of-range one; a bad-index
	// directive consumes nothing, so a later implicit verb — including
	// a `*` width and another %w — still reads gc's operand, while an
	// operand run-out there prints %!verb(MISSING).
	fmt.Println(fmt.Errorf("a %[0]*w b", 7, fmt.Errorf("e0")))
	fmt.Println(fmt.Errorf("%[0]*w %w", 7, fmt.Errorf("e0")))
	fmt.Println(fmt.Errorf("%[9]w %*s", "x", 5, "ab"))
	fmt.Println(fmt.Errorf("%[9]w %3s", "x", "ab"))
	fmt.Println(fmt.Errorf("%[9]w %*.*s", "x", 8, 3, "abcdef"))
	fmt.Println(fmt.Errorf("%[9]w %w %w", "x", fmt.Errorf("e1"), fmt.Errorf("e2")))
	fmt.Println(fmt.Errorf("%[2]*[1]w %w %w", "x", 9, fmt.Errorf("e1"), fmt.Errorf("e2")))
	fmt.Println(fmt.Errorf("%[2]*w %d", "x"))
}

type S struct{}

func (S) String() string { return "STR" }
