package main

type D int

// a case clause spells gc's own wording — `invalid case s in switch on
// d (mismatched types string and D)`, not the binary-op message.
func main() {
	var d D
	var s string
	switch d {
	case s:
	}
}
