package main

import (
	"fmt"
	"reflect"
)

func main() {
	n := uint(7)
	var a int8 = (1 << n) / 3
	var b int8 = (1 << n) >> 1
	var c int8 = 100 / (1 << n)
	var d int8 = ((1 << n) / 3) + 1
	var e int8 = -((1 << n) / 3)
	var f int = (1 << n) / 3
	var g int8 = (1 << n) % 5
	var h int8 = (1 << n) * 2
	fmt.Println(a, b, c, d, e, f, g, h)
	fmt.Println(8>>((1<<n)>>1), reflect.Kind((1<<n)/2) == reflect.Int)
	var k reflect.Kind = (1 << n) / 2
	fmt.Println(k)
	m := uint(2)
	fmt.Println(8>>((1<<m)>>1), reflect.Kind((1<<m)/2) == reflect.Int)
	if (1<<n)/999 == 0 {
		fmt.Println("zero")
	}
}
