package main

import "fmt"

type Tsmallp byte

func (p *Tsmallp) M(x int) int { return x + int(*p) }

func main() {
	sp := Tsmallp(2)
	psp := &sp
	psp = nil
	f := psp.M
	fmt.Println(f != nil)
	s := []int{1}
	s = nil
	fmt.Println(s == nil, len(s))
}
