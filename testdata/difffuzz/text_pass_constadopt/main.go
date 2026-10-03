package main

import "fmt"

func u16(ii int) uint16 {
	var i = uint16(ii)
	i = 'a' + i%26
	i |= i << 8
	return i
}

func main() {
	// an untyped rune/int const adopts the other operand's type —
	// 'a' + uint16var is uint16 arithmetic ($GOROOT/test/copy.go u16).
	fmt.Println(u16(0), u16(1), u16(25))
	var b byte = 3
	fmt.Println(b+1, 10-b, b*2)
	var f32 float32 = 1.5
	fmt.Println(f32+0.25, 2.0*f32)
	type MyInt int16
	var m MyInt = 7
	fmt.Println(m+3, m-'a'+'a')
}
