package main

import "fmt"

// An untyped constant in a binary op adopts the other operand's type —
// and gc rejects the program when the constant cannot be represented
// in it: `300 - v8`, `1.5 + v8`, `v8 == 300`, `v8 < 300`,
// `(1+2i) - v8`, `1e300 + f32` all fail to compile (minigo rejects
// them as runtime traps, which pins cannot express since gc never
// runs them). These are the representable edges that must keep
// evaluating.

func main() {
	var v8 int8 = 8
	var u8 uint8 = 250
	var f32 float32 = 1.5

	// representable adoptions — the constant still types to the operand
	fmt.Println(v8+100, v8 == 127, v8 < 127) // 108 false true
	fmt.Println(u8-250, u8 == 250, 255-u8)   // 0 true 5
	fmt.Println('a' + v8)                    // 105 — rune const adopts int8
	fmt.Println(1.0+v8, 1e2-v8)              // 9 92 — integral float consts adopt
	fmt.Println(f32+2.5, f32*2)              // 4 3 — float32 adoption
	fmt.Println(v8+1, u8+2)                  // 9 252 — var ops keep wrapping at width

	// mismatched-kind consts stay type errors in gc, but these compare
	// legal-typed pairs
	fmt.Println(v8 == 8, f32 == 1.5) // true true

	// named types adopt too
	type Small int8
	var s Small = 7
	fmt.Println(s + 100) // 107
}
