package main

import (
	"fmt"
	"unicode/utf8"
)

// statically mismatched operands trap like gc's compile reject — the
// ops here are all rejected by `go build` on the same program. `==`/`!=`
// stay dynamic (an `any`-carried value reaches the same instruction and
// interface equality is lawful), and a bare numeric side is
// domain-ambiguous (host returns, under-typed generic arguments), so
// those residuals keep evaluating.
type duration int64

type mystr string

// two named operands of different typedefs — `x + y` on Duration and
// int64 is a type error in Go.
func NamedNamedAdd() int64 {
	var d duration
	var i64 int64
	return int64(d + i64)
}

func NamedNamedOrder() bool {
	var d duration
	var i64 int64
	return d < i64
}

type yourstr string

func NamedNamedStr() string {
	var ms mystr
	var ys yourstr
	return string(ms + ys)
}

// a named operand against a host-carried scalar of a different domain —
// `ms * utf8.RuneCount` rejects like `mystr * int`.
func NamedBareCrossDomain() string {
	var ms mystr
	return string(ms * utf8.RuneCount([]byte("ab")))
}

// a named operand against a host-carried bool — `s + utf8.ValidString`
// rejects like `string + bool`.
func NamedBareBool() string {
	var s string
	_ = s + utf8.ValidString("x")
	return s
}

// a string constant can't adopt a numeric declared type — `d < "x"`
// fails like gc's `cannot use "x" as duration`.
func ConstStringIntoNum() bool {
	var d duration
	return d < "x"
}

// an untyped rune constant is not assignable to a string type — gc
// rejects `ms + 'x'`; only an explicit string('x') converts.
func ConstRuneIntoStr() string {
	var ms mystr
	return string(ms + 'x')
}

// ---- residuals that keep evaluating (documented divergences) ----

// `==`/`!=` evaluate: an `any`-carried value reaches the same compare
// and interface equality is lawful, so statically-mismatched pairs get
// dynamic-`false` instead of gc's reject.
func EqlMismatchEval() bool {
	var d duration
	var i64 int64 = 5
	return d == i64 // gc: invalid operation — minigo: false
}

func EqlMismatchStrEval() bool {
	var ms mystr = "x"
	var s string = "x"
	return ms == s // gc: invalid operation — minigo: true (same underlying)
}

// a bare numeric side is domain-ambiguous: `len`'s int and an
// under-typed generic argument look alike — `d < len` keeps evaluating
// where gc rejects `duration < int`.
func OrderBareNumEval() bool {
	var d duration = 3
	return d < len("ab") // gc rejects; minigo's residual divergence
}

// ---- gc-legal behavior that must keep working ----

// the escape hatch: an explicit conversion makes the operand's type
// match, exactly like in Go.
func ConvertEscape() bool {
	var d duration = 2
	return d < duration(len("abc")) && duration(9) != d
}

// a named operand with a same-domain untyped constant adapts — gc's
// constant rules, unchanged.
func NamedConstAdopt() int64 {
	var d duration = 5
	return int64(d + 1)
}

// two named operands of the same typedef evaluate their underlying.
func SameTypedefAdd() int64 {
	var a, b duration = 3, 4
	return int64(a + b)
}

// an untyped package constant adapts to a byte operand like a literal.
func PkgConstAdopt() bool {
	var b [4]byte
	b[0] = 200
	return b[0] < utf8.RuneSelf // gc: byte < 0x80 -> false
}

// iface-carried equality stays dynamic: `one() == two()` compares
// (type, value) pairs — both operands are `any`.
func IfaceCarriedEql() bool {
	one := func() any { return duration(1) }
	two := func() any { return duration(1) }
	other := func() any { return int64(1) }
	return one() == two() && !(one() == other())
}

func main() { fmt.Println(NamedNamedAdd()) }
