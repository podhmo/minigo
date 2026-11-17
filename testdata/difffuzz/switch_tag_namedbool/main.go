package main

type B bool

// a comparison's untyped bool result pins to bool as a switch tag, so
// a named-bool case expr mismatches it (gc: invalid case).
func main() {
	x, y := 3, 5
	switch x < y {
	case B(true):
	}
}
