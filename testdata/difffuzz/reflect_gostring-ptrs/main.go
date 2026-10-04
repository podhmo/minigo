package main

import (
	"fmt"
	"strings"
)

func add(a, b int) int { return a + b }

type F func(int) int

var fi F = func(x int) int { return x }

var r_i0 = 42

func main() {
	ch := make(chan int, 1)
	ptr := &r_i0
	f := func() {}
	fmt.Printf("%T\n", add)
	fmt.Printf("%T\n", f)
	fmt.Printf("%T\n", fi)
	fmt.Printf("%T\n", fmt.Println)
	fmt.Printf("%T\n", strings.Contains)
	fmt.Println(add != nil && ptr != nil && ch != nil)
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%#v", add), "(func(int, int) int)(0x"))
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%#v", ch), "(chan int)(0x"))
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%#v", ptr), "(*int)(0x"))
	fmt.Println(strings.HasPrefix(fmt.Sprintf("%#v", f), "(func())(0x"))
	fmt.Println(strings.HasSuffix(fmt.Sprintf("%#v", add), ")"))
}
