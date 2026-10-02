package main

import "fmt"

type MyI16 int16
type MyU8 uint8

func conv(x uint32) MyI16 { return MyI16(x) }

func main() {
	var u32 uint32 = 5
	var m MyI16 = 2
	var c MyI16 = MyI16(u32)
	fmt.Printf("%T %v\n", c^m, c^m)
	fmt.Printf("%T %v\n", MyI16(u32)^m, MyI16(u32)^m)
	fmt.Printf("%T %v\n", conv(u32)^m, conv(u32)^m)
	fmt.Printf("%T %v\n", c+c, c+c)
	fmt.Printf("%T %v\n", c-c, c-c)
	var i16 int16 = 1
	fmt.Printf("%T %v\n", m+MyI16(i16), m+MyI16(i16))
	var mu MyU8 = 3
	fmt.Printf("%T %v\n", mu+MyU8(i16), mu+MyU8(i16))
	mu = MyU8(i16)
	mu++
	fmt.Printf("%T %v\n", mu, mu)
	mu--
	fmt.Printf("%T %v\n", mu, mu)
}
