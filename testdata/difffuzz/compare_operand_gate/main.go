package main

import (
	"fmt"
	"unicode/utf8"
)

// comparison operands under the assignability gate — every line is a
// program `go build` accepts: same-typedef pairs, named-with-untyped-
// const adoption, untyped package constants, interface dynamic
// equality, and conversion escapes. (Mismatched pairs can't pin: gc
// won't compile them — testdata/mismatchedops holds the trap side.)
type duration int64

type mystr string

func one() any   { return duration(1) }
func two() any   { return duration(1) }
func three() any { return int64(1) }

func main() {
	var d, e duration = 2, 5
	var ms, ns mystr = "x", "y"
	fmt.Println(d+e, d*e, d < e, d == e) // same typedef: 7 10 true false
	fmt.Println(ms+ns, ms == ns)         // xy false
	fmt.Println(d+1, d == 5, ms == "x")  // const adoption: 3 true true
	fmt.Println(d < duration(9))         // conversion escape: true
	var b [4]byte
	b[0] = 200
	fmt.Println(b[0] < utf8.RuneSelf) // pkg const adapts: false
	fmt.Println(b[0]+1 < utf8.UTFMax) // 201 < 4: false
	var i64 int64 = 9
	fmt.Println(i64+int64(len("ab")), i64 < 10)   // 11 true
	fmt.Println(one() == two(), one() == three()) // iface dynamic: true false
	var x any = ms
	fmt.Println(x == any(ms), x == any("x")) // true false
}
