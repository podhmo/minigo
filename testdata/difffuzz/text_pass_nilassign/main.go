package main

type Tsmallp byte

func (p *Tsmallp) M(x int) int { return x + int(*p) }

func main() {
	sp := Tsmallp(2)
	psp := &sp
	psp = nil
	f := psp.M
	println(f != nil)
	s := []int{1}
	s = nil
	println(s == nil, len(s))
}
