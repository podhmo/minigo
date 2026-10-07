package main

import "fmt"

// Typed constant expressions evaluate in the declared type's exact
// domain: gc rejects when the result is unrepresentable —
// `uint(4) - 8` → "constant -4 of type uint overflows uint",
// `uint8(250) + 10`, `int8(120) + 10`, `uint8(1) << 8`, `-uint8(5)`
// (minigo traps them as runtime errors, which pins cannot express
// since gc never runs them). The same op on a var wraps at the
// declared width instead — both semantics coexist here.

const C uint8 = 200

func main() {
	// representable constant expressions keep evaluating exactly
	fmt.Println(uint8(7)/2, uint8(1)<<7, uint8(200) == 56) // 3 128 false
	fmt.Println(^uint8(0), ^int8(0), -uint(0))             // 255 -1 0 — ^ always fits
	fmt.Println(uint8(0xF0)&0x0F, uint8(0xF0)|0x0F, ^uint8(0xF0))
	fmt.Println(int8(100)+20, uint8(200)+55) // 120 255
	fmt.Println(1+uint8(200), uint8(5)*2)    // 201 10
	fmt.Println(uint8(7)%3, int8(-100)-20)   // 1 -120
	fmt.Println(C+55, C-200)                 // 255 0 — named const exprs

	// named declared types participate too
	type MyU uint8
	fmt.Println(MyU(200)+50, MyU(0xF0)>>4) // 250 15

	// the RUNTIME halves wrap: vars materialize, so the same arithmetic
	// is ordinary width-wrapping — not a constant expression
	var v8 int8 = 100
	var u8 uint8 = 200
	fmt.Println(v8+100, -v8, u8-8, u8+100, -u8) // -56 -100 192 44 56
}
