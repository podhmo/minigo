package main

import "fmt"

type Number *Number

func use(p *Number) *Number { return p }

type Tsmallv byte

func main() {
	var n Number
	fmt.Println(use(n) == nil)
	fmt.Println(int(Tsmallv(5)))
}
