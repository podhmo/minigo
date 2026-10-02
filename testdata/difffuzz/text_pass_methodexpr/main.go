package main

import "fmt"

type I interface{ m() int }

type S struct{ v int }

func (s S) m() int { return s.v }
func (s *S) inc()  { s.v++ }

type U struct{ I }

func main() {
	fv := S.m
	fmt.Println(fv(S{5}))
	fp := (*S).inc
	s := S{1}
	fp(&s)
	fmt.Println(s.v)
	fi := I.m
	fmt.Println(fi(S{7}))
	fu := U.m
	fmt.Println(fu(U{I: S{9}}))
}
