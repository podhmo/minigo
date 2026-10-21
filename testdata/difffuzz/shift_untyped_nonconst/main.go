package main

import "fmt"

// An untyped constant shifted by a NON-constant count is no constant
// expression: Go types the constant from the context (int by default)
// and shifts at that width, so `1 << n` with n = 70 is 0 — not gc's
// "constant overflows int" — and a narrower target truncates the same
// way (`var y uint8 = 1 << 8` is 0).

func main() {
	n := uint(70)
	s := uint(3)
	fmt.Println(1<<n, 1<<s, 1<<n == 0, 1<<s == 8)       // 0 8 true true
	fmt.Println((1<<n)+1, -(1 << n), ^(1 << n), 1<<s*3) // 1 0 -1 24

	var x int64 = 1 << s
	var y uint8 = 1 << (s + 5)
	var z int8 = 1 << (s + 4)
	var w uint64 = 1 << (s + 60)
	var w2 int64 = 1 << (s + 60)
	fmt.Println(x, y, z, w, w2) // 8 0 -128 9223372036854775808 -9223372036854775808

	fmt.Println(uint8(1)<<(s+5), uint8(1<<(s+5)), int8(1<<(s+4))) // 0 0 -128

	k := 1 << n
	j := 1<<s + 1
	fmt.Printf("%T %v %T %v\n", k, k, j, j) // int 0 int 9

	// division, remainder and >> settle at int first
	fmt.Println((1<<(s+61))/3, (1<<(s+61))>>1, (1<<(s+61))%7) // 0 0 0

	var r rune = 'a' << s
	fmt.Println(r, 'a'<<s) // 776 776

	// constant shifts stay constant expressions
	const big = 1 << 100
	fmt.Println(big>>98, 1<<62) // 4 4611686018427387904

	// a huge count never builds the exact big integer
	h := uint(1) << 40
	var h8 uint8 = 3 << h
	fmt.Println(1<<h, 1<<h == 0, (1<<h)+5, h8, 7>>h, -(1 << h), 1<<n<<h) // 0 true 5 0 0 0 0
	fmt.Println(3<<(s+59)*3, 3<<(s+59)+3<<(s+59), ^(5 << (s + 59)))      // wraps at int

	arr := []int{10, 20, 30}
	fmt.Println(arr[1<<(s-2)], len(make([]byte, 1<<s))) // 30 8
	for i := uint(0); i < 3; i++ {
		fmt.Print(1<<i, " ")
	}
	fmt.Println()
}
